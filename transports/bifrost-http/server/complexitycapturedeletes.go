package server

import (
	"context"
	"time"

	"github.com/maximhq/bifrost/framework/logstore"
	"github.com/maximhq/bifrost/plugins/routing/complexity"
)

// captureDeleteRecordTimeout bounds recording deletes made while no routing
// plugin is loaded.
const captureDeleteRecordTimeout = 5 * time.Second

// complexityCaptureDeletes keeps the complexity router's failed captured-vector
// deletes in the log store's model_routing_vector_deletes table. It is an
// adapter rather than a log store method so the routing plugin does not import
// the log store.
type complexityCaptureDeletes struct {
	store logstore.ComplexitySuggestionStore
}

var _ complexity.CaptureDeleteRecorder = complexityCaptureDeletes{}

// AddCaptureDeletes records deletes to retry.
func (d complexityCaptureDeletes) AddCaptureDeletes(ctx context.Context, rows []complexity.CaptureDelete) error {
	if len(rows) == 0 {
		return nil
	}
	records := make([]logstore.ComplexityVectorDelete, len(rows))
	for i, row := range rows {
		records[i] = logstore.ComplexityVectorDelete{LogID: row.LogID, Namespace: row.Namespace}
	}
	return d.store.AddComplexityVectorDeletes(ctx, records)
}

// ListCaptureDeletes returns up to limit deletes to retry, oldest first.
func (d complexityCaptureDeletes) ListCaptureDeletes(ctx context.Context, limit int) ([]complexity.CaptureDelete, error) {
	records, err := d.store.ListComplexityVectorDeletes(ctx, limit)
	if err != nil || len(records) == 0 {
		return nil, err
	}
	rows := make([]complexity.CaptureDelete, len(records))
	for i, record := range records {
		rows[i] = complexity.CaptureDelete{LogID: record.LogID, Namespace: record.Namespace}
	}
	return rows, nil
}

// DeleteCaptureDeletes drops the deletes that have now gone through.
func (d complexityCaptureDeletes) DeleteCaptureDeletes(ctx context.Context, logIDs []string) error {
	return d.store.DeleteComplexityVectorDeletes(ctx, logIDs)
}

// captureDeleteRecorderSetter is a plugin that keeps failed captured-vector
// deletes somewhere durable; the routing plugin's request capture is one.
type captureDeleteRecorderSetter interface {
	SetComplexityCaptureDeleteRecorder(recorder complexity.CaptureDeleteRecorder)
}

// applyRoutingCaptureDeleteRecorder gives a plugin the log store to keep its
// failed captured-vector deletes in, for its upkeep to retry, as Warp's are.
func (s *BifrostHTTPServer) applyRoutingCaptureDeleteRecorder(plugin any) {
	setter, ok := plugin.(captureDeleteRecorderSetter)
	if !ok || s.Config == nil || s.Config.LogsStore == nil {
		return
	}
	setter.SetComplexityCaptureDeleteRecorder(complexityCaptureDeletes{store: s.Config.LogsStore})
}

// forgetCapturedLogs is the log-delete subscription for captured request
// vectors. The routing plugin is looked up per delete rather than held: a
// reload replaces the instance, and the subscription must follow it.
//
// With no routing plugin (removed, disabled, or never loaded) the deletes
// are recorded for retry with no namespace, so the next plugin's upkeep
// removes them from its serving capture namespace. They are recorded only
// when the vector store holds a capture namespace: captures are written
// nowhere else, so otherwise there is nothing to forget, and a deployment
// that never captured gains no rows. Like the plugin's Forget, this never
// fails the log delete, which has already happened.
func (s *BifrostHTTPServer) forgetCapturedLogs(ctx context.Context, ids []string) {
	if len(ids) == 0 {
		return
	}
	if routingPlugin, err := s.getRoutingPlugin(); err == nil {
		routingPlugin.ForgetComplexityCapturedRequests(ctx, ids)
		return
	}
	if s.Config == nil || s.Config.VectorStore == nil || s.Config.LogsStore == nil {
		return
	}
	// A context of its own: the caller's may already be done.
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), captureDeleteRecordTimeout)
	defer cancel()
	namespaces, err := s.Config.VectorStore.ListNamespaces(recordCtx, complexity.CaptureNamespacePrefix+"_")
	if err == nil && len(namespaces) == 0 {
		return
	}
	// A failed listing records anyway: an extra row costs one retry, a
	// missing one leaves the vector until the lookback sweeps it.
	rows := make([]complexity.CaptureDelete, len(ids))
	for i, id := range ids {
		rows[i] = complexity.CaptureDelete{LogID: id}
	}
	if err := (complexityCaptureDeletes{store: s.Config.LogsStore}).AddCaptureDeletes(recordCtx, rows); err != nil {
		logger.Warn("no routing plugin to remove the captured vectors of %d deleted log(s), and they could not be recorded to retry: %v", len(ids), err)
	}
}
