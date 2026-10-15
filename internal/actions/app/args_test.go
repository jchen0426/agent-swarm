package app_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/actions/app"
)

// A redaction path that selects a bounded argument hides it in the stored
// record, but the bounds check reads the argument as the agent wrote it, and
// the server receives it so.
func TestARedactedArgumentIsCheckedAndSentAsWritten(t *testing.T) {
	e := newEnv(t)
	files := revisionFiles(t, defaultPins(), "reviewed")
	files["redaction-rules/default-secrets.yaml"] = []byte(`kind: RedactionRule
metadata: {name: default-secrets, owner: security@example.com}
spec:
  patterns: ['AKIA[0-9A-Z]{16}']
  paths: ['$..password', '$.window']
`)
	rev := build(t, e.cc, "5ec0d5ec0d5ec0d5ec0d5ec0d5ec0d5ec0d5ec0", files)
	e.revs.add(rev)
	e.session.Commit = rev.Commit()
	e.sessions.Put(token, &e.session)

	r := e.call(t, "flags__get_flag", `{"flag":"x","window":"1h"}`)

	require.Equal(t, app.StatusOK, r.Status, r.Text)
	rec, _, _, _ := e.audit.Record(r.CallID)
	assert.JSONEq(t, `{"flag":"x","window":"[redacted:default-secrets]"}`, string(rec.Args))
	sameBytes(t, `{"flag":"x","window":"1h"}`, e.up.sent()[0].args)
}
