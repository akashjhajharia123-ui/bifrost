package complexity

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	bifrost "github.com/maximhq/bifrost/core"
	"github.com/maximhq/bifrost/core/schemas"
	"github.com/maximhq/bifrost/framework/vectorstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newCaptureTestStore is an in-memory store that, like Qdrant and Weaviate,
// accepts only UUID point IDs.
func newCaptureTestStore(t testing.TB) vectorstore.VectorStore {
	t.Helper()
	store, err := vectorstore.NewVectorStore(context.Background(), &vectorstore.Config{
		Enabled: true, Type: vectorstore.VectorStoreTypeChromem, Config: vectorstore.ChromemConfig{},
	}, bifrost.NewDefaultLogger(schemas.LogLevelError))
	require.NoError(t, err)
	return &uuidOnlyStore{store}
}

type uuidOnlyStore struct{ vectorstore.VectorStore }

func requireUUID(id string) error {
	if _, err := uuid.Parse(id); err != nil {
		return fmt.Errorf("invalid id format (must be UUID): %w", err)
	}
	return nil
}

func (s uuidOnlyStore) Add(ctx context.Context, namespace, id string, embedding []float32, metadata map[string]any) error {
	if err := requireUUID(id); err != nil {
		return err
	}
	return s.VectorStore.Add(ctx, namespace, id, embedding, metadata)
}

func (s uuidOnlyStore) Delete(ctx context.Context, namespace, id string) error {
	if err := requireUUID(id); err != nil {
		return err
	}
	return s.VectorStore.Delete(ctx, namespace, id)
}

func (s uuidOnlyStore) GetChunk(ctx context.Context, namespace, id string) (vectorstore.SearchResult, error) {
	if err := requireUUID(id); err != nil {
		return vectorstore.SearchResult{}, err
	}
	return s.VectorStore.GetChunk(ctx, namespace, id)
}

func newCaptureTest(t testing.TB) *RequestCapture {
	t.Helper()
	c := NewRequestCapture(bifrost.NewDefaultLogger(schemas.LogLevelError))
	t.Cleanup(c.Close)
	return c
}

// newStoppedCapture returns a capture with no writer, so every sample stays
// countable as queued or dropped.
func newStoppedCapture() *RequestCapture {
	ctx, cancel := context.WithCancel(context.Background())
	return &RequestCapture{queue: make(chan captureItem, captureQueueSize), done: make(chan struct{}), ctx: ctx, cancel: cancel, drainBudget: time.Second}
}

func requestCtx(id string) context.Context {
	return context.WithValue(context.Background(), schemas.BifrostContextKeyRequestID, id)
}

func waitWritten(t *testing.T, c *RequestCapture, n int64) {
	t.Helper()
	require.Eventually(t, func() bool { return c.Stats().Written >= n }, 2*time.Second, 5*time.Millisecond,
		"capture wrote %d of %d (stats %+v)", c.Stats().Written, n, c.Stats())
}

func TestCaptureNamespaceIsKeyedByEmbeddingSpace(t *testing.T) {
	a := CaptureNamespace("openai", "text-embedding-3-small", 1536, 1)
	assert.Equal(t, a, CaptureNamespace("openai", "text-embedding-3-small", 1536, 1))
	assert.NotEqual(t, a, CaptureNamespace("openai", "text-embedding-3-large", 1536, 1))
	assert.NotEqual(t, a, CaptureNamespace("openai", "text-embedding-3-small", 512, 1))
	assert.NotEqual(t, a, CaptureNamespace("openai", "text-embedding-3-small", 1536, 3), "embedding more history is a different input")
	assert.Equal(t, a, CaptureNamespace("openai", "text-embedding-3-small", 1536, 0), "an unset history count embeds the last message, as 1 does")
	assert.True(t, strings.HasPrefix(a, CaptureNamespacePrefix+"_"))
	// The generation registry reclaims unclaimed namespaces under the
	// semantic prefix; capture must never be mistaken for one.
	assert.False(t, strings.HasPrefix(a, SemanticVectorStoreNamespace))
}

func TestRequestCaptureOffCapturesNothing(t *testing.T) {
	c := newCaptureTest(t)
	store := newCaptureTestStore(t)
	c.offer(requestCtx("r1"), store, "ns", 2, []float32{1, 0})
	c.Configure(&CaptureConfig{SampleRate: 0})
	c.offer(requestCtx("r2"), store, "ns", 2, []float32{1, 0})
	assert.Equal(t, CaptureStats{}, c.Stats())

	var nilCapture *RequestCapture
	nilCapture.offer(requestCtx("r3"), store, "ns", 2, []float32{1, 0}) // must not panic
	assert.False(t, nilCapture.Enabled())
}

// A captured vector carries no routing verdict: that is one router's view of
// the request and lives on its log, while the vector can serve every router
// with the same embedding space.
func TestRequestCaptureWritesVectorWithoutRoutingVerdict(t *testing.T) {
	c := newCaptureTest(t)
	store := newCaptureTestStore(t)
	ns := CaptureNamespace("openai", "m", 2, 1)
	c.Configure(&CaptureConfig{SampleRate: 1})
	c.offer(requestCtx("req-1"), store, ns, 2, []float32{0.6, 0.8})
	waitWritten(t, c, 1)

	got, err := store.GetChunk(context.Background(), ns, capturePointID("req-1"))
	require.NoError(t, err)
	assert.Equal(t, captureMetadataKindValue, got.Properties[captureMetadataKind])
	assert.NotZero(t, got.Properties[captureMetadataTimestamp])
	assert.Equal(t, "req-1", got.Properties[captureMetadataLogID], "the log ID is kept for lookup")
	for _, key := range []string{"tier", "score_bp", "accepted"} {
		assert.NotContains(t, got.Properties, key)
	}
}

func TestRequestCaptureSkipsRequestsWithoutID(t *testing.T) {
	c := newCaptureTest(t)
	c.Configure(&CaptureConfig{SampleRate: 1})
	c.offer(context.Background(), newCaptureTestStore(t), "ns", 2, []float32{1, 0})
	assert.Zero(t, c.Stats().Queued, "a vector without a request ID can never be joined to its log")
}

func TestRequestCaptureSamplesAtConfiguredRate(t *testing.T) {
	c := newStoppedCapture()
	c.Configure(&CaptureConfig{SampleRate: 0.25})
	store := newCaptureTestStore(t)
	const n = 4000
	for range n {
		c.offer(requestCtx("r"), store, "ns", 2, []float32{1, 0})
	}
	got := c.Stats().Queued + c.Stats().Dropped
	assert.InDelta(t, n/4, got, n/20, "sampled %d of %d at rate 0.25", got, n)
}

func TestRequestCaptureDropsWhenQueueIsFull(t *testing.T) {
	c := newStoppedCapture()
	c.Configure(&CaptureConfig{SampleRate: 1})
	store := newCaptureTestStore(t)
	for range captureQueueSize + 10 {
		c.offer(requestCtx("r"), store, "ns", 2, []float32{1, 0})
	}
	st := c.Stats()
	assert.EqualValues(t, captureQueueSize, st.Queued)
	assert.EqualValues(t, 10, st.Dropped, "a full queue must drop, never block the request")
}

// Around an embedding-space switch the writer sees old- and new-space samples
// interleaved, and two nodes can serve different spaces at once. Writing to
// one space must never delete another: its vectors may still be live.
func TestRequestCaptureKeepsOtherEmbeddingSpacesOnWrite(t *testing.T) {
	c := newCaptureTest(t)
	store := newCaptureTestStore(t)
	oldNS, newNS := CaptureNamespace("openai", "old", 2, 1), CaptureNamespace("openai", "new", 2, 1)
	c.Configure(&CaptureConfig{SampleRate: 1})
	c.offer(requestCtx("a"), store, oldNS, 2, []float32{1, 0})
	waitWritten(t, c, 1)
	c.offer(requestCtx("b"), store, newNS, 2, []float32{0, 1})
	waitWritten(t, c, 2)
	c.offer(requestCtx("c"), store, oldNS, 2, []float32{1, 0})
	waitWritten(t, c, 3)

	ctx := context.Background()
	for ns, id := range map[string]string{oldNS: "a", newNS: "b"} {
		_, err := store.GetChunk(ctx, ns, capturePointID(id))
		assert.NoError(t, err, "vector %s in %s must survive writes to the other space", id, ns)
	}
}

// Stale embedding spaces age out through Sweep like the serving one, even
// when nothing is serving, so no captured vector outlives log retention.
func TestRequestCaptureSweepCoversStaleEmbeddingSpaces(t *testing.T) {
	c := newCaptureTest(t)
	store := newCaptureTestStore(t)
	oldNS, newNS := CaptureNamespace("openai", "old", 2, 1), CaptureNamespace("openai", "new", 2, 1)
	c.Configure(&CaptureConfig{SampleRate: 1})
	c.offer(requestCtx("a"), store, oldNS, 2, []float32{1, 0})
	c.offer(requestCtx("b"), store, newNS, 2, []float32{0, 1})
	waitWritten(t, c, 2)
	ctx := context.Background()

	n, err := c.Sweep(ctx, store, newNS, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.Zero(t, n, "vectors younger than retention stay in every space")

	n, err = c.Sweep(ctx, store, "", time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 2, n, "with nothing serving, every space still ages out")
	_, err = store.GetChunk(ctx, oldNS, capturePointID("a"))
	assert.Error(t, err)
}

func TestRequestCaptureForgetAndSweep(t *testing.T) {
	c := newCaptureTest(t)
	store := newCaptureTestStore(t)
	ns := CaptureNamespace("openai", "m", 2, 1)
	c.Configure(&CaptureConfig{SampleRate: 1})
	for _, id := range []string{"keep", "forget", "old"} {
		c.offer(requestCtx(id), store, ns, 2, []float32{1, 0})
	}
	waitWritten(t, c, 3)
	ctx := context.Background()

	c.Forget(ctx, store, ns, []string{"forget", "never-captured"})
	_, err := store.GetChunk(ctx, ns, capturePointID("forget"))
	assert.Error(t, err, "a deleted log's vector must go")
	_, err = store.GetChunk(ctx, ns, capturePointID("keep"))
	assert.NoError(t, err, "a kept log's vector must stay")

	// Everything was written just now, so a cutoff in the past keeps it and
	// one in the future removes it.
	n, err := c.Sweep(ctx, store, ns, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	assert.Zero(t, n)
	n, err = c.Sweep(ctx, store, ns, time.Now().Add(time.Hour))
	require.NoError(t, err)
	assert.Equal(t, 2, n)

	// Nothing to act on is not an error.
	c.Forget(ctx, nil, "", []string{"x"})
	n, err = c.Sweep(ctx, store, CaptureNamespace("openai", "never-written", 2, 1), time.Now())
	assert.NoError(t, err)
	assert.Zero(t, n)
}

// A namespace that fails to sweep does not stop the namespaces after it from
// aging out; the failure is still reported once every namespace was tried.
func TestRequestCaptureSweepContinuesPastFailedNamespace(t *testing.T) {
	c := newCaptureTest(t)
	inner := newCaptureTestStore(t)
	nsA, nsB := CaptureNamespace("openai", "a", 2, 1), CaptureNamespace("openai", "b", 2, 1)
	c.Configure(&CaptureConfig{SampleRate: 1})
	c.offer(requestCtx("x"), inner, nsA, 2, []float32{1, 0})
	c.offer(requestCtx("y"), inner, nsB, 2, []float32{0, 1})
	waitWritten(t, c, 2)
	ctx := context.Background()
	// Sweep walks namespaces in listing order; fail whichever comes first.
	namespaces, err := inner.ListNamespaces(ctx, CaptureNamespacePrefix+"_")
	require.NoError(t, err)
	require.Len(t, namespaces, 2)
	failNS, okNS := namespaces[0], namespaces[1]
	failID, okID := "x", "y"
	if failNS != nsA {
		failID, okID = okID, failID
	}
	store := &flakyStore{VectorStore: inner, failDeleteAll: map[string]bool{failNS: true}}

	n, err := c.Sweep(ctx, store, "", time.Now().Add(time.Hour))
	require.Error(t, err)
	assert.Contains(t, err.Error(), failNS)
	assert.Equal(t, 1, n, "the later namespace is still swept")
	_, err = inner.GetChunk(ctx, okNS, capturePointID(okID))
	assert.Error(t, err, "the later namespace's expired vector must go")
	_, err = inner.GetChunk(ctx, failNS, capturePointID(failID))
	assert.NoError(t, err)
}

// DeleteAll can succeed as a call while some deletes failed; Sweep counts only
// the ones that went and reports the rest.
func TestRequestCaptureSweepCountsOnlySuccessfulDeletes(t *testing.T) {
	c := newCaptureTest(t)
	inner := newCaptureTestStore(t)
	ns := CaptureNamespace("openai", "m", 2, 1)
	c.Configure(&CaptureConfig{SampleRate: 1})
	for _, id := range []string{"a", "b"} {
		c.offer(requestCtx(id), inner, ns, 2, []float32{1, 0})
	}
	waitWritten(t, c, 2)
	store := &flakyStore{VectorStore: inner, failResults: map[string]bool{ns: true}}

	n, err := c.Sweep(context.Background(), store, ns, time.Now().Add(time.Hour))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "locked")
	assert.Equal(t, 2, n, "only successful deletes count")
}

// ceilingStore behaves like Redis and Weaviate under a large window: a
// filtered read stops at a result ceiling and then reports nothing more,
// which looks exactly like the end of the data. Its unfiltered walk is
// complete but, like a Redis key walk, may hand an entry back twice.
type ceilingStore struct {
	vectorstore.VectorStore
	ceiling int
}

func (c *ceilingStore) GetAll(ctx context.Context, namespace string, queries []vectorstore.Query, selectFields []string, cursor *string, limit int64) ([]vectorstore.SearchResult, *string, error) {
	results, next, err := c.VectorStore.GetAll(ctx, namespace, queries, selectFields, cursor, limit)
	if err != nil {
		return nil, nil, err
	}
	if len(queries) > 0 {
		if len(results) > c.ceiling {
			results = results[:c.ceiling]
		}
		return results, nil, nil
	}
	return append(results, results...), next, nil
}

// A suggestion run must see every request captured in its window, however
// many there are: a backend's cap on filtered reads must not cut the window
// short, and an entry a walk returns twice is still one request.
func TestEachCapturedReadsTheWholeWindowPastAFilteredReadCeiling(t *testing.T) {
	c := newCaptureTest(t)
	inner := newCaptureTestStore(t)
	ns := CaptureNamespace("openai", "m", 2, 1)
	c.Configure(&CaptureConfig{SampleRate: 1})
	ids := []string{"r1", "r2", "r3", "r4", "r5"}
	for _, id := range ids {
		c.offer(requestCtx(id), inner, ns, 2, []float32{1, 0})
	}
	waitWritten(t, c, int64(len(ids)))
	store := &ceilingStore{VectorStore: inner, ceiling: 2}

	now := time.Now()
	var got []string
	require.NoError(t, EachCaptured(context.Background(), store, ns, now.Add(-time.Hour), now.Add(time.Minute), func(page []CapturedRequest) error {
		for _, r := range page {
			got = append(got, r.LogID)
			assert.Equal(t, []float32{1, 0}, r.Vector)
		}
		return nil
	}))
	assert.ElementsMatch(t, ids, got, "every captured request, once")

	got = nil
	require.NoError(t, EachCaptured(context.Background(), store, ns, now.Add(-2*time.Hour), now.Add(-time.Hour), func(page []CapturedRequest) error {
		for _, r := range page {
			got = append(got, r.LogID)
		}
		return nil
	}))
	assert.Empty(t, got, "the window still applies")
}

// BenchmarkRequestCaptureOffer isolates the hook's request-path cost from the
// writer: off, sampled onto the queue (a drainer discards), and sampled with
// the queue full (dropped).
func BenchmarkRequestCaptureOffer(b *testing.B) {
	store := newCaptureTestStore(b)
	ctx := requestCtx("bench")
	vec := make([]float32, 1536)
	run := func(b *testing.B, c *RequestCapture) {
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			c.offer(ctx, store, "ns", 1536, vec)
		}
	}
	b.Run("off", func(b *testing.B) {
		c := newCaptureTest(b)
		run(b, c)
	})
	b.Run("on_queued", func(b *testing.B) {
		c := newStoppedCapture()
		c.Configure(&CaptureConfig{SampleRate: 1})
		stop := make(chan struct{})
		go func() {
			for {
				select {
				case <-c.queue:
				case <-stop:
					return
				}
			}
		}()
		defer close(stop)
		run(b, c)
	})
	b.Run("on_dropped", func(b *testing.B) {
		c := newStoppedCapture()
		c.Configure(&CaptureConfig{SampleRate: 1})
		for range captureQueueSize {
			c.offer(ctx, store, "ns", 1536, vec)
		}
		run(b, c)
	})
	b.Run("sampled_out_1pct", func(b *testing.B) {
		c := newStoppedCapture()
		c.Configure(&CaptureConfig{SampleRate: 0.01})
		run(b, c)
	})
}

// memoryDeletes is an in-memory CaptureDeleteRecorder.
type memoryDeletes struct{ rows []CaptureDelete }

func (m *memoryDeletes) AddCaptureDeletes(_ context.Context, rows []CaptureDelete) error {
	m.rows = append(m.rows, rows...)
	return nil
}

func (m *memoryDeletes) ListCaptureDeletes(_ context.Context, limit int) ([]CaptureDelete, error) {
	return m.rows[:min(limit, len(m.rows))], nil
}

func (m *memoryDeletes) DeleteCaptureDeletes(_ context.Context, ids []string) error {
	gone := map[string]bool{}
	for _, id := range ids {
		gone[id] = true
	}
	kept := m.rows[:0]
	for _, r := range m.rows {
		if !gone[r.LogID] {
			kept = append(kept, r)
		}
	}
	m.rows = kept
	return nil
}

// flakyStore refuses deletes while down. failDeleteAll names namespaces whose
// DeleteAll returns an error; failResults names namespaces whose DeleteAll
// succeeds as a call but reports one failed result alongside the real ones.
type flakyStore struct {
	vectorstore.VectorStore
	down          bool
	deletes       int
	failDeleteAll map[string]bool
	failResults   map[string]bool
}

func (f *flakyStore) DeleteAll(ctx context.Context, namespace string, queries []vectorstore.Query) ([]vectorstore.DeleteResult, error) {
	if f.failDeleteAll[namespace] {
		return nil, errors.New("vector store unavailable")
	}
	results, err := f.VectorStore.DeleteAll(ctx, namespace, queries)
	if err == nil && f.failResults[namespace] {
		results = append(results, vectorstore.DeleteResult{ID: "stuck", Status: vectorstore.DeleteStatusError, Error: "locked"})
	}
	return results, err
}

func (f *flakyStore) Delete(ctx context.Context, namespace, id string) error {
	f.deletes++
	if f.down {
		return errors.New("vector store unavailable")
	}
	return f.VectorStore.Delete(ctx, namespace, id)
}

func TestRequestCaptureRecordsFailedDeletesAndRetriesThem(t *testing.T) {
	c := newCaptureTest(t)
	inner := newCaptureTestStore(t)
	ns := CaptureNamespace("openai", "m", 2, 1)
	c.Configure(&CaptureConfig{SampleRate: 1})
	for _, id := range []string{"a", "b", "c"} {
		c.offer(requestCtx(id), inner, ns, 2, []float32{1, 0})
	}
	waitWritten(t, c, 3)
	ctx := context.Background()
	recorder := &memoryDeletes{}
	c.SetDeleteRecorder(recorder)
	store := &flakyStore{VectorStore: inner, down: true}

	c.Forget(ctx, store, ns, []string{"a", "b", "c"})
	assert.Equal(t, 1, store.deletes, "after the first refusal the rest are recorded without trying")
	require.Len(t, recorder.rows, 3)
	assert.Equal(t, ns, recorder.rows[0].Namespace)

	// Still down: the pass stops at the first failure and keeps every row.
	n, err := c.RetryDeletes(ctx, store, ns)
	assert.Error(t, err)
	assert.Zero(t, n)
	assert.Len(t, recorder.rows, 3)

	store.down = false
	recorder.rows = append(recorder.rows, CaptureDelete{LogID: "a", Namespace: ns}) // recorded twice
	n, err = c.RetryDeletes(ctx, store, ns)
	require.NoError(t, err)
	assert.Equal(t, 3, n)
	assert.Empty(t, recorder.rows)
	for _, id := range []string{"a", "b", "c"} {
		_, err := inner.GetChunk(ctx, ns, capturePointID(id))
		assert.Error(t, err, "%s should be gone after the retry", id)
	}
}

func TestRequestCaptureRetryResolvesUnknownNamespace(t *testing.T) {
	c := newCaptureTest(t)
	store := newCaptureTestStore(t)
	ns := CaptureNamespace("openai", "m", 2, 1)
	c.Configure(&CaptureConfig{SampleRate: 1})
	c.offer(requestCtx("a"), store, ns, 2, []float32{1, 0})
	waitWritten(t, c, 1)
	recorder := &memoryDeletes{}
	c.SetDeleteRecorder(recorder)

	// Nothing was serving when the log was deleted, so no namespace was known.
	c.Forget(context.Background(), store, "", []string{"a"})
	require.Equal(t, []CaptureDelete{{LogID: "a"}}, recorder.rows)
	n, err := c.RetryDeletes(context.Background(), store, ns)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

func TestCapturePointIDIsStableUUID(t *testing.T) {
	gatewayID := uuid.NewString()
	assert.Equal(t, gatewayID, capturePointID(gatewayID), "a UUID request ID is its own point ID")
	assert.Equal(t, gatewayID, capturePointID(strings.ToUpper(gatewayID)), "in canonical form")

	for _, id := range []string{"req-1", "trace/abc:42", "  "} {
		got := capturePointID(id)
		_, err := uuid.Parse(got)
		require.NoError(t, err, "%q must map to a UUID", id)
		assert.Equal(t, got, capturePointID(id), "%q must map to the same point every time", id)
	}
	assert.NotEqual(t, capturePointID("req-1"), capturePointID("req-2"))
}

// A missing namespace means the vector is already gone: RetryDeletes clears
// that row and goes on to the rows after it, instead of stopping on it.
func TestRequestCaptureRetrySkipsAbsentNamespace(t *testing.T) {
	c := newCaptureTest(t)
	store := newCaptureTestStore(t)
	ns := CaptureNamespace("openai", "m", 2, 1)
	absent := CaptureNamespace("openai", "never-written", 2, 1)
	c.Configure(&CaptureConfig{SampleRate: 1})
	c.offer(requestCtx("later"), store, ns, 2, []float32{1, 0})
	waitWritten(t, c, 1)
	recorder := &memoryDeletes{rows: []CaptureDelete{
		{LogID: "first", Namespace: absent},
		{LogID: "later", Namespace: ns},
	}}
	c.SetDeleteRecorder(recorder)

	n, err := c.RetryDeletes(context.Background(), store, ns)
	require.NoError(t, err)
	assert.Equal(t, 2, n)
	assert.Empty(t, recorder.rows)
	_, err = store.GetChunk(context.Background(), ns, capturePointID("later"))
	assert.Error(t, err, "the row after the absent namespace must still be deleted")
}

func TestRequestCaptureCloseDrainsQueuedSamples(t *testing.T) {
	store := newCaptureTestStore(t)
	ns := CaptureNamespace("openai", "m", 2, 1)
	c := NewRequestCapture(bifrost.NewDefaultLogger(schemas.LogLevelError))
	c.Configure(&CaptureConfig{SampleRate: 1})
	for i := range 50 {
		c.offer(requestCtx(fmt.Sprintf("r%d", i)), store, ns, 2, []float32{1, 0})
	}
	c.Close()
	st := c.Stats()
	assert.EqualValues(t, st.Queued, st.Written+st.Failed, "every accepted sample is written before Close returns")
	assert.EqualValues(t, 50, st.Written)

	c.offer(requestCtx("late"), store, ns, 2, []float32{1, 0})
	assert.EqualValues(t, 50, c.Stats().Queued, "a closed capture refuses new samples")
}
