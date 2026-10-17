package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jchen0426/agent-swarm/internal/audit/app"
	"github.com/jchen0426/agent-swarm/internal/audit/chain"
	"github.com/jchen0426/agent-swarm/internal/audit/dirstore"
	"github.com/jchen0426/agent-swarm/internal/audit/postgres"
	"github.com/jchen0426/agent-swarm/internal/audit/s3store"
	"github.com/jchen0426/agent-swarm/internal/db"
	"github.com/jchen0426/agent-swarm/internal/secrets/files"
)

// auditFlags are the flags both audit commands take.
type auditFlags struct {
	database, schema, tenant, anchors, secrets string
	every                                      time.Duration
}

func (f *auditFlags) add(cmd *cobra.Command) {
	fl := cmd.Flags()
	fl.StringVar(&f.database, "database-url", "", "Postgres URL of a role that can only read; when empty, the libpq environment variables (PGHOST, PGUSER, ...)")
	fl.StringVar(&f.schema, "schema", "public", "the schema that holds the platform's tables")
	fl.StringVar(&f.tenant, "tenant", "", "the tenant whose chain to anchor or verify")
	fl.StringVar(&f.anchors, "anchors", "", "the anchor store: s3://BUCKET/PREFIX?endpoint=HOST&key=REF&secret=REF, or file:///DIR")
	fl.StringVar(&f.secrets, "secrets", "", "directory the secret references of an s3:// store resolve in")
	_ = cmd.MarkFlagRequired("tenant")
	_ = cmd.MarkFlagRequired("anchors")
}

func newAuditCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Anchor the audit chain outside the database, and verify it",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errNoCommand
		},
	}
	cmd.AddCommand(newAnchorCommand(), newVerifyCommand())
	return cmd
}

func newAnchorCommand() *cobra.Command {
	var f auditFlags
	cmd := &cobra.Command{
		Use:   "anchor --tenant ID --anchors URL",
		Short: "Write the audit chain's head to the anchor store",
		Long: `Write one anchor to the anchor store (spec Section 9.4): the head of the
tenant's audit chain, the runs whose records the chain gained since the anchor
before, and the rows each audited table holds, all read from one snapshot.

The store's owner runs it, outside the builders, every hour, also when nothing
happened, so that verification can tell a quiet hour from anchoring that
stopped. It needs the owner's credential for the store and a database role that
can only read; the platform holds neither.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			var an chain.Anchor
			err := withAuditor(c.Context(), &f, func(a *app.Auditor) error {
				var err error
				an, err = a.Anchor(c.Context())
				return err
			})
			if err != nil {
				return failure{err}
			}
			return writeJSON(c.OutOrStdout(), struct {
				Key  string `json:"key"`
				Seq  int64  `json:"seq"`
				Runs int    `json:"runs"`
			}{an.Key(), an.Seq, len(an.Runs)})
		},
	}
	f.add(cmd)
	return cmd
}

func newVerifyCommand() *cobra.Command {
	var f auditFlags
	cmd := &cobra.Command{
		Use:   "verify --tenant ID --anchors URL",
		Short: "Check the audit chain, its rows and its anchors",
		Long: `Read the tenant's whole audit chain, every row it covers and every anchor,
every version of each, and report what disagrees (spec Sections 8.4 and 9.4):
records that do not link, rows changed, deleted or never chained, anchors the
chain no longer matches or has lost records past, as after a restore, anchors
that conflict or are hidden, anchoring that stopped, and a store that is not
write-once. The findings are printed as JSON. Any finding exits with 3, and a
verification that could not run with 1.

A record is anchored only once an anchor follows it, and a missed hour stays a
finding, so the owner's scheduler runs audit anchor every hour and retries
within ten minutes.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			var findings []chain.Finding
			err := withAuditor(c.Context(), &f, func(a *app.Auditor) error {
				var err error
				findings, err = a.Verify(c.Context(), f.every)
				return err
			})
			if err != nil {
				return failure{err}
			}
			if err := writeJSON(c.OutOrStdout(), report(findings)); err != nil {
				return err
			}
			if len(findings) > 0 {
				return found{fmt.Errorf("%d findings", len(findings))}
			}
			return nil
		},
	}
	f.add(cmd)
	cmd.Flags().DurationVar(&f.every, "every", time.Hour, "the anchor interval")
	return cmd
}

// finding is a finding as verify prints it.
type finding struct {
	Kind    chain.Kind `json:"kind"`
	Seq     int64      `json:"seq,omitempty"`
	Through int64      `json:"through,omitempty"`
	Source  string     `json:"source,omitempty"`
	Key     string     `json:"key,omitempty"`
	Anchor  string     `json:"anchor,omitempty"`
	Runs    []string   `json:"runs,omitempty"`
	Detail  string     `json:"detail"`
}

func report(found []chain.Finding) any {
	out := make([]finding, 0, len(found))
	for i := range found {
		f := &found[i]
		out = append(out, finding{f.Kind, f.Seq, f.Through, f.Source, f.Key, f.Anchor, f.Runs, f.Detail})
	}
	return struct {
		Findings []finding `json:"findings"`
	}{out}
}

// withAuditor opens the anchor store and the database, and calls fn with
// the tenant's auditor. A tenant id that cannot name anchors is refused
// first.
func withAuditor(ctx context.Context, f *auditFlags, fn func(*app.Auditor) error) error {
	if !chain.ValidTenant(f.tenant) {
		return fmt.Errorf("--tenant: %q is not a tenant id: lowercase letters, digits and dashes", f.tenant)
	}
	store, closeStore, err := openAnchors(ctx, f.anchors, f.secrets)
	if err != nil {
		return err
	}
	defer closeStore()
	pool, err := db.Open(ctx, f.database)
	if err != nil {
		return err
	}
	defer pool.Close()
	a, err := app.New(postgres.NewChain(pool, f.tenant, f.schema), store, f.tenant, time.Now)
	if err != nil {
		return err
	}
	return fn(a)
}

// openAnchors opens the anchor store a URL names.
func openAnchors(ctx context.Context, raw, secretsDir string) (app.Anchors, func(), error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("--anchors: %w", err)
	}
	switch u.Scheme {
	case "file":
		if u.Host != "" || u.RawQuery != "" || u.Path == "" {
			return nil, nil, errors.New("--anchors: a file store is file:///DIR")
		}
		s, err := dirstore.Open(u.Path)
		if err != nil {
			return nil, nil, err
		}
		return s, func() { _ = s.Close() }, nil
	case "s3":
		s, err := openS3(ctx, u, secretsDir)
		if err != nil {
			return nil, nil, err
		}
		return s, func() {}, nil
	}
	return nil, nil, fmt.Errorf("--anchors: %q is neither s3:// nor file://", u.Scheme)
}

// openS3 opens an s3:// store: the bucket is the host, the prefix the path;
// endpoint, region and insecure are query parameters, and key and secret are
// secret references that resolve in secretsDir.
func openS3(ctx context.Context, u *url.URL, secretsDir string) (*s3store.Store, error) {
	q := u.Query()
	for name := range q {
		switch name {
		case "endpoint", "region", "insecure", "key", "secret":
		default:
			return nil, fmt.Errorf("--anchors: an s3:// store takes no %q", name)
		}
	}
	insecure := false
	if v := q.Get("insecure"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("--anchors: insecure: %w", err)
		}
		insecure = b
	}
	if secretsDir == "" {
		return nil, errors.New("--secrets: an s3:// store's key resolves in a secrets directory")
	}
	secrets, err := files.Open(secretsDir)
	if err != nil {
		return nil, err
	}
	defer func() { _ = secrets.Close() }()
	keyID, err := secrets.Secret(ctx, q.Get("key"))
	if err != nil {
		return nil, fmt.Errorf("--anchors: key: %w", err)
	}
	secretKey, err := secrets.Secret(ctx, q.Get("secret"))
	if err != nil {
		return nil, fmt.Errorf("--anchors: secret: %w", err)
	}
	prefix := strings.Trim(u.Path, "/")
	if prefix != "" {
		prefix += "/"
	}
	return s3store.New(&s3store.Config{Endpoint: q.Get("endpoint"), Insecure: insecure, Region: q.Get("region"), Bucket: u.Host, Prefix: prefix}, keyID, secretKey)
}
