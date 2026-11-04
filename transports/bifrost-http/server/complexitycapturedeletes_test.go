package server

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/maximhq/bifrost/plugins/routing"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
	"github.com/maximhq/bifrost/transports/v2/bifrost-http/lib"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The capture's failed deletes land in model_routing_vector_deletes and come
// back as they went in, namespace included, until the retry clears them.
func TestComplexityCaptureDeletesRoundTripThroughLogStore(t *testing.T) {
	ctx := context.Background()
	ls, err := logstore.NewLogStore(ctx, &logstore.Config{
		Enabled: true, Type: logstore.LogStoreTypeSQLite,
		Config: &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "logs.db")},
	}, noopTestLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ls.Close(ctx) })
	recorder := complexityCaptureDeletes{store: ls}

	require.NoError(t, recorder.AddCaptureDeletes(ctx, nil))
	require.NoError(t, recorder.AddCaptureDeletes(ctx, []complexity.CaptureDelete{
		{LogID: "a", Namespace: "BifrostComplexityRequests_abc"},
		{LogID: "b"},
	}))
	rows, err := recorder.ListCaptureDeletes(ctx, 10)
	require.NoError(t, err)
	assert.ElementsMatch(t, []complexity.CaptureDelete{
		{LogID: "a", Namespace: "BifrostComplexityRequests_abc"},
		{LogID: "b"},
	}, rows)

	require.NoError(t, recorder.DeleteCaptureDeletes(ctx, []string{"a"}))
	rows, err = recorder.ListCaptureDeletes(ctx, 10)
	require.NoError(t, err)
	assert.Equal(t, []complexity.CaptureDelete{{LogID: "b"}}, rows)

	// Warp's retry list is a different table: neither sees the other's rows.
	warp, err := ls.ListWarpVectorDeletes(ctx, 10)
	require.NoError(t, err)
	assert.Empty(t, warp)
}

// The routing plugin keeps its failed deletes through this setter; if it
// stopped satisfying it, bootstrap and reload would both skip it silently.
var _ captureDeleteRecorderSetter = (*routing.RoutingPlugin)(nil)

type deleteRecorderSetter struct {
	recorder complexity.CaptureDeleteRecorder
}

func (d *deleteRecorderSetter) SetComplexityCaptureDeleteRecorder(r complexity.CaptureDeleteRecorder) {
	d.recorder = r
}

func newCaptureDeletesTestLogStore(t *testing.T) logstore.LogStore {
	t.Helper()
	ctx := context.Background()
	ls, err := logstore.NewLogStore(ctx, &logstore.Config{
		Enabled: true, Type: logstore.LogStoreTypeSQLite,
		Config: &logstore.SQLiteConfig{Path: filepath.Join(t.TempDir(), "logs.db")},
	}, noopTestLogger{})
	require.NoError(t, err)
	t.Cleanup(func() { _ = ls.Close(ctx) })
	return ls
}

// Bootstrap and reload both hand the routing plugin the log store to keep
// failed deletes in; without a log store there is nowhere to keep them.
func TestApplyRoutingCaptureDeleteRecorder(t *testing.T) {
	setter := &deleteRecorderSetter{}
	(&BifrostHTTPServer{Config: &lib.Config{}}).applyRoutingCaptureDeleteRecorder(setter)
	assert.Nil(t, setter.recorder, "no log store, no recorder")

	ls := newCaptureDeletesTestLogStore(t)
	(&BifrostHTTPServer{Config: &lib.Config{LogsStore: ls}}).applyRoutingCaptureDeleteRecorder(setter)
	require.NotNil(t, setter.recorder)
	assert.Equal(t, complexityCaptureDeletes{store: ls}, setter.recorder)

	(&BifrostHTTPServer{Config: &lib.Config{LogsStore: ls}}).applyRoutingCaptureDeleteRecorder(struct{}{}) // left alone
}

// A log deleted while the routing plugin is absent (removed, disabled, or
// mid-replacement) still has its captured vector removed: the delete is
// recorded beside the logs, namespace left for the serving one, and the
// plugin's upkeep retries it once a plugin is back. With a plugin registered,
// the delete is the plugin's, and nothing is recorded here.
func TestForgetCapturedLogsWithoutRoutingPluginRecordsForRetry(t *testing.T) {
	ctx := context.Background()
	ls := newCaptureDeletesTestLogStore(t)
	vs, err := vectorstore.NewVectorStore(ctx, &vectorstore.Config{
		Enabled: true, Type: vectorstore.VectorStoreTypeChromem, Config: vectorstore.ChromemConfig{},
	}, noopTestLogger{})
	require.NoError(t, err)
	cfg := &lib.Config{LogsStore: ls, VectorStore: vs}
	s := &BifrostHTTPServer{Config: cfg}
	recorder := complexityCaptureDeletes{store: ls}
	recorded := func() []complexity.CaptureDelete {
		rows, err := recorder.ListCaptureDeletes(ctx, 10)
		require.NoError(t, err)
		return rows
	}
	setPlugins := func(plugins ...schemas.BasePlugin) { cfg.BasePlugins.Store(&plugins) }

	// Nothing was ever captured: there is nothing to forget, so no row.
	setPlugins()
	s.forgetCapturedLogs(ctx, []string{"never-captured"})
	assert.Empty(t, recorded(), "a store with no capture namespace records nothing")

	ns := complexity.CaptureNamespace("openai", "m", 2, 1)
	require.NoError(t, vs.CreateNamespace(ctx, ns, 2, map[string]vectorstore.VectorStoreProperties{}))

	// The plugin is registered: the delete goes to it.
	setPlugins(&routing.RoutingPlugin{})
	s.forgetCapturedLogs(ctx, []string{"with-plugin"})
	assert.Empty(t, recorded(), "a registered plugin owns the delete")

	// The plugin was removed: the delete is kept for the next plugin.
	setPlugins()
	s.forgetCapturedLogs(ctx, []string{"a", "b"})
	assert.ElementsMatch(t, []complexity.CaptureDelete{{LogID: "a"}, {LogID: "b"}}, recorded())

	// No vector store at all: captures never existed.
	(&BifrostHTTPServer{Config: &lib.Config{LogsStore: ls}}).forgetCapturedLogs(ctx, []string{"c"})
	assert.Len(t, recorded(), 2)
}
