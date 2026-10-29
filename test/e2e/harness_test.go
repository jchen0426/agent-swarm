//go:build e2e

package e2e

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/test/e2e/harness"
)

// pgSSLRequest is the first message a Postgres client may send: its length, 8,
// and the code 80877103, which asks whether the server speaks TLS. A server
// answers one byte, S or N.
var pgSSLRequest = []byte{0, 0, 0, 8, 0x04, 0xd2, 0x16, 0x2f}

// answers reports whether the Postgres at addr answers an SSLRequest.
func answers(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := conn.Write(pgSSLRequest); err != nil {
		return false
	}
	var reply [1]byte
	if _, err := io.ReadFull(conn, reply[:]); err != nil {
		return false
	}
	return reply[0] == 'S' || reply[0] == 'N'
}

// hostPort returns the address a database URL names.
func hostPort(t *testing.T, databaseURL string) string {
	t.Helper()
	u, err := url.Parse(databaseURL)
	require.NoError(t, err)
	require.NotEmpty(t, u.Host)
	return u.Host
}

// docker runs the docker command line and returns what it prints.
func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.CommandContext(t.Context(), "docker", args...).Output()
	require.NoError(t, err, "docker %s", strings.Join(args, " "))
	return string(out)
}

func containerLabels(t *testing.T, id string) map[string]string {
	t.Helper()
	require.NotEmpty(t, id)
	var labels map[string]string
	require.NoError(t, json.Unmarshal([]byte(docker(t, "container", "inspect", "--format", "{{json .Config.Labels}}", id)), &labels))
	return labels
}

// containerNetworks returns the names of the networks a container is on.
func containerNetworks(t *testing.T, id string) []string {
	t.Helper()
	var networks map[string]json.RawMessage
	require.NoError(t, json.Unmarshal([]byte(docker(t, "container", "inspect", "--format", "{{json .NetworkSettings.Networks}}", id)), &networks))
	names := make([]string, 0, len(networks))
	for name := range networks {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// projectContainers returns the ids of the running containers that carry the
// project label.
func projectContainers(t *testing.T) []string {
	t.Helper()
	ids := strings.Fields(docker(t, "ps", "--quiet", "--filter", "label=agentswarm.project=agent-swarm"))
	slices.Sort(ids)
	return ids
}

func requireGone(t *testing.T, id string) {
	t.Helper()
	require.NotEmpty(t, id)
	err := exec.CommandContext(t.Context(), "docker", "container", "inspect", id).Run()
	require.Error(t, err, "container %s is still there", id)
}

func TestTheHarnessContainersCarryTheProjectLabel(t *testing.T) {
	var postgresID, toxiproxyID string
	t.Run("harness", func(t *testing.T) {
		env := harness.New(t, harness.Options{Toxiproxy: true})
		postgresID, toxiproxyID = env.PostgresID(), env.ToxiproxyID()

		postgres := containerLabels(t, postgresID)
		assert.Equal(t, "agent-swarm", postgres["agentswarm.project"])
		assert.Equal(t, "/var/lib/postgresql/data", postgres["agentswarm.data"])
		proxy := containerLabels(t, toxiproxyID)
		assert.Equal(t, "agent-swarm", proxy["agentswarm.project"])
		assert.NotContains(t, proxy, "agentswarm.data", "a toxiproxy holds no data worth keeping")

		networks := containerNetworks(t, postgresID)
		require.Len(t, networks, 1)
		assert.Equal(t, networks, containerNetworks(t, toxiproxyID), "Postgres and the toxiproxy share one network")
	})

	// The subtest's cleanup removed both containers.
	require.NotEmpty(t, postgresID)
	requireGone(t, postgresID)
	requireGone(t, toxiproxyID)
}

func TestTheToxiproxyCutsAndRestoresAnInstancesDatabase(t *testing.T) {
	env := harness.New(t, harness.Options{Toxiproxy: true})
	a, b := hostPort(t, env.DatabaseURL("a")), hostPort(t, env.DatabaseURL("b"))
	require.NotEqual(t, a, b, "each instance has a proxy of its own")
	require.NotEqual(t, a, hostPort(t, env.DatabaseURL("")), "an instance does not reach the database directly")
	require.True(t, answers(a), "the SSLRequest through a's proxy")
	require.True(t, answers(b), "the SSLRequest through b's proxy")

	env.Cut(t, "a")

	assert.False(t, answers(a), "after Cut a's proxy answers nothing")
	assert.True(t, answers(b), "b's proxy is not cut")
	assert.True(t, answers(hostPort(t, env.DatabaseURL(""))), "the database itself is not cut")

	env.Restore(t, "a")

	assert.True(t, answers(a), "after Restore a's proxy answers again")
}

// silent reports whether no byte comes on conn within a second while the
// connection stays open: the read runs out of time rather than ending.
func silent(t *testing.T, conn net.Conn) bool {
	t.Helper()
	require.NoError(t, conn.SetDeadline(time.Now().Add(time.Second)))
	var reply [1]byte
	_, err := conn.Read(reply[:])
	var timeout net.Error
	return errors.As(err, &timeout) && timeout.Timeout()
}

// ask sends an SSLRequest on conn, which must be the connection's first
// message the server receives.
func ask(t *testing.T, conn net.Conn) {
	t.Helper()
	require.NoError(t, conn.SetDeadline(time.Now().Add(time.Second)))
	_, err := conn.Write(pgSSLRequest)
	require.NoError(t, err)
}

// answered reports whether the server answers an SSLRequest on conn.
func answered(t *testing.T, conn net.Conn) bool {
	t.Helper()
	ask(t, conn)
	require.NoError(t, conn.SetDeadline(time.Now().Add(2*time.Second)))
	var reply [1]byte
	_, err := io.ReadFull(conn, reply[:])
	return err == nil && (reply[0] == 'S' || reply[0] == 'N')
}

// Blackhole makes an instance's proxy drop every byte, both ways, and close
// nothing, as a database whose address went dark does: a connection the proxy
// had, and one it accepts meanwhile, are answered nothing, and neither ends,
// while the other instance's proxy goes on. Restore lifts it: a new
// connection is answered again, and so is the connection the proxy kept, but
// what was sent on it while it was blackholed stays lost, as on a path that
// came back.
func TestTheToxiproxyBlackholesAnInstancesDatabase(t *testing.T) {
	env := harness.New(t, harness.Options{Toxiproxy: true})
	a, b := hostPort(t, env.DatabaseURL("a")), hostPort(t, env.DatabaseURL("b"))
	require.True(t, answers(a), "the SSLRequest through a's proxy")
	require.True(t, answers(b), "the SSLRequest through b's proxy")
	before, err := net.DialTimeout("tcp", a, 2*time.Second)
	require.NoError(t, err)
	defer before.Close()

	env.Blackhole(t, "a")

	ask(t, before)
	assert.True(t, silent(t, before), "a connection a's proxy had is answered nothing, and stays open")
	during, err := net.DialTimeout("tcp", a, 2*time.Second)
	require.NoError(t, err, "a's proxy accepts a connection")
	defer during.Close()
	ask(t, during)
	assert.True(t, silent(t, during), "a connection a's proxy accepted is answered nothing, and stays open")
	assert.True(t, answers(b), "b's proxy is not blackholed")
	assert.True(t, answers(hostPort(t, env.DatabaseURL(""))), "the database itself is not blackholed")

	env.Restore(t, "a")

	assert.True(t, answers(a), "after Restore a's proxy answers a new connection")
	assert.True(t, silent(t, before), "the request sent while blackholed stays lost")
	assert.True(t, answered(t, before), "the connection the proxy kept is open, and answered again")
}

func TestTheHarnessUsesTheDatabaseItIsGiven(t *testing.T) {
	given := harness.New(t, harness.Options{})
	require.NotEmpty(t, given.PostgresID())
	t.Setenv("AGENTSWARM_E2E_DATABASE", given.DatabaseURL(""))
	t.Setenv("AGENTSWARM_E2E_DATABASE_CONTAINER", given.PostgresID())
	before := projectContainers(t)

	env := harness.New(t, harness.Options{Toxiproxy: true})

	assert.Empty(t, env.PostgresID(), "the harness started no Postgres")
	assert.Empty(t, env.ToxiproxyID(), "nor a toxiproxy")
	assert.Equal(t, before, projectContainers(t), "no container was started")
	assert.Equal(t, given.DatabaseURL(""), env.DatabaseURL("a"), "every instance gets the given database")
	assert.Equal(t, given.DatabaseURL(""), env.DatabaseURL("b"))
	env.Psql(t, "create table harness_given (n int); insert into harness_given values (7)")
	assert.Equal(t, "7", given.Psql(t, "select n from harness_given"), "Psql reaches the given database")
	assert.Equal(t, "7", env.Psql(t, "select n from harness_given"))
}

// A test's cleanup can run psql, whose deadline is its own: the test's context
// has ended by then. So a test can leave a database it was given (R17) as it
// found it.
func TestPsqlRunsInATestsCleanup(t *testing.T) {
	env := harness.New(t, harness.Options{})
	env.Psql(t, "create table harness_cleaned (n int)")

	t.Run("a test that adds a row and removes it when it ends", func(t *testing.T) {
		env.Psql(t, "insert into harness_cleaned values (1)")
		t.Cleanup(func() { env.Psql(t, "delete from harness_cleaned") })
	})

	assert.Equal(t, "0", env.Psql(t, "select count(*) from harness_cleaned"), "the subtest's cleanup removed its row")
}

func TestPsqlReturnsWhatItPrints(t *testing.T) {
	env := harness.New(t, harness.Options{})

	assert.Equal(t, "3", env.Psql(t, "select 1 + 2"))
	assert.Empty(t, env.Psql(t, "create table harness_rows (a int, b text)"), "a statement prints nothing")
	env.Psql(t, "insert into harness_rows values (1, 'x'), (2, 'y')")
	assert.Equal(t, "1|x\n2|y", env.Psql(t, "select a, b from harness_rows order by a"), "rows are unaligned and without a header")
	assert.Equal(t, "2", env.Psql(t, "select count(*) from harness_rows; "), "a trailing semicolon and blank are fine")
	assert.Equal(t, "2\n5", env.Psql(t, "select count(*) from harness_rows; select 5"), "each statement prints its rows")
}
