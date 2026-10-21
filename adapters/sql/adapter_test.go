package sql

import (
	"context"
	"database/sql"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/thecodearcher/limen"

	_ "modernc.org/sqlite"
)

func setupTestDB(t *testing.T) *Adapter {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS "test_items" (
		"id" INTEGER PRIMARY KEY AUTOINCREMENT,
		"name" TEXT NOT NULL,
		"email" TEXT,
		"age" INTEGER DEFAULT 0,
		"status" TEXT DEFAULT 'active'
	)`)
	if err != nil {
		t.Fatalf("failed to create table: %v", err)
	}

	return NewSQLite(db)
}

func TestCreate_And_FindOne(t *testing.T) {
	t.Parallel()

	adapter := setupTestDB(t)
	ctx := context.Background()

	dbResult, err := adapter.Create(ctx, "test_items", map[string]any{
		"name":  "Alice",
		"email": "alice@example.com",
		"age":   30,
	})
	assert.NoError(t, err)
	assert.Equal(t, int64(1), dbResult.RowsAffected)

	result, err := adapter.FindOne(ctx, "test_items", []limen.Where{
		limen.Eq("name", "Alice"),
	}, nil)
	assert.NoError(t, err)
	assert.Equal(t, "Alice", result["name"])
	assert.Equal(t, "alice@example.com", result["email"])
}

func TestFindOne_NotFound(t *testing.T) {
	t.Parallel()

	adapter := setupTestDB(t)
	ctx := context.Background()

	_, err := adapter.FindOne(ctx, "test_items", []limen.Where{
		limen.Eq("name", "Nonexistent"),
	}, nil)
	assert.ErrorIs(t, err, limen.ErrRecordNotFound)
}

func TestFindMany(t *testing.T) {
	t.Parallel()

	adapter := setupTestDB(t)
	ctx := context.Background()

	for _, name := range []string{"Alice", "Bob", "Charlie"} {
		_, err := adapter.Create(ctx, "test_items", map[string]any{"name": name, "email": name + "@test.com"})
		assert.NoError(t, err)
	}

	results, err := adapter.FindMany(ctx, "test_items", nil, nil)
	assert.NoError(t, err)
	assert.Len(t, results, 3)

	alice := results[0]
	assert.Equal(t, "Alice", alice["name"])
	assert.Equal(t, "Alice@test.com", alice["email"])

	bob := results[1]
	assert.Equal(t, "Bob", bob["name"])
	assert.Equal(t, "Bob@test.com", bob["email"])

	charlie := results[2]
	assert.Equal(t, "Charlie", charlie["name"])
	assert.Equal(t, "Charlie@test.com", charlie["email"])
}

func TestFindMany_WithOptions(t *testing.T) {
	t.Parallel()

	adapter := setupTestDB(t)
	ctx := context.Background()

	for _, name := range []string{"Bob", "Alice", "Charlie", "Diana"} {
		adapter.Create(ctx, "test_items", map[string]any{"name": name})
	}

	results, err := adapter.FindMany(ctx, "test_items", nil, &limen.QueryOptions{
		Limit:  2,
		Offset: 1,
		OrderBy: []limen.OrderBy{
			{Column: "name", Direction: limen.OrderByAsc},
		},
	})
	assert.NoError(t, err)
	assert.Len(t, results, 2)
	assert.Equal(t, "Bob", results[0]["name"])
	assert.Equal(t, "Charlie", results[1]["name"])
}

func TestUpdate(t *testing.T) {
	t.Parallel()

	adapter := setupTestDB(t)
	ctx := context.Background()

	adapter.Create(ctx, "test_items", map[string]any{"name": "Alice", "email": "old@test.com"})

	dbResult, err := adapter.Update(ctx, "test_items", []limen.Where{
		limen.Eq("name", "Alice"),
	}, map[string]any{"email": "new@test.com"})
	assert.NoError(t, err)
	assert.Equal(t, int64(1), dbResult.RowsAffected)

	result, _ := adapter.FindOne(ctx, "test_items", []limen.Where{limen.Eq("name", "Alice")}, nil)
	assert.Equal(t, "new@test.com", result["email"])
}

func TestCreateUpdate_MarshalsMapValues(t *testing.T) {
	t.Parallel()

	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	_, err = db.Exec(`CREATE TABLE "sessions" (
		"id" INTEGER PRIMARY KEY AUTOINCREMENT,
		"token" TEXT NOT NULL,
		"metadata" TEXT
	)`)
	require.NoError(t, err)

	adapter := NewSQLite(db)
	ctx := t.Context()

	_, err = adapter.Create(ctx, "sessions", map[string]any{
		"token": "t1",
		"metadata": map[string]any{
			"active_organization_id": 23,
		},
	})
	require.NoError(t, err)

	row, err := adapter.FindOne(ctx, "sessions", []limen.Where{limen.Eq("token", "t1")}, nil)
	require.NoError(t, err)
	assert.Equal(t, `{"active_organization_id":23}`, row["metadata"])

	_, err = adapter.Update(ctx, "sessions", []limen.Where{
		limen.Eq("token", "t1"),
	}, map[string]any{
		"metadata": map[string]any{"active_organization_id": 24, "ip": "::1"},
	})
	require.NoError(t, err)

	row, err = adapter.FindOne(ctx, "sessions", []limen.Where{limen.Eq("token", "t1")}, nil)
	require.NoError(t, err)
	assert.Equal(t, `{"active_organization_id":24,"ip":"::1"}`, row["metadata"])
}

func TestUpdate_MixesArithmeticAndAssignments(t *testing.T) {
	t.Parallel()

	adapter := setupTestDB(t)
	ctx := t.Context()
	_, err := adapter.Create(ctx, "test_items", map[string]any{
		"name":   "Alice",
		"age":    10,
		"status": "active",
	})
	require.NoError(t, err)

	_, err = adapter.Update(ctx, "test_items", []limen.Where{
		limen.Eq("name", "Alice"),
	}, map[string]any{
		"age":    limen.IncrementBy(5),
		"status": "updated",
	})
	require.NoError(t, err)

	_, err = adapter.Update(ctx, "test_items", []limen.Where{
		limen.Eq("name", "Alice"),
	}, map[string]any{
		"age": limen.DecrementBy(3),
	})
	require.NoError(t, err)

	result, err := adapter.FindOne(ctx, "test_items", []limen.Where{
		limen.Eq("name", "Alice"),
	}, nil)
	require.NoError(t, err)
	assert.EqualValues(t, 12, result["age"])
	assert.Equal(t, "updated", result["status"])
}

func TestUpdate_ConcurrentIncrements(t *testing.T) {
	t.Parallel()

	adapter := setupTestDB(t)
	ctx := t.Context()
	_, err := adapter.Create(ctx, "test_items", map[string]any{
		"name": "counter",
		"age":  0,
	})
	require.NoError(t, err)

	const increments = 50
	var wg sync.WaitGroup
	errs := make(chan error, increments)
	for range increments {
		wg.Go(func() {
			_, err := adapter.Update(ctx, "test_items", []limen.Where{
				limen.Eq("name", "counter"),
			}, map[string]any{
				"age": limen.IncrementBy(1),
			})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		require.NoError(t, err)
	}
	result, err := adapter.FindOne(ctx, "test_items", []limen.Where{
		limen.Eq("name", "counter"),
	}, nil)
	require.NoError(t, err)
	assert.EqualValues(t, increments, result["age"])
}

func TestDelete(t *testing.T) {
	t.Parallel()

	adapter := setupTestDB(t)
	ctx := context.Background()

	adapter.Create(ctx, "test_items", map[string]any{"name": "ToDelete"})

	dbResult, err := adapter.Delete(ctx, "test_items", []limen.Where{
		limen.Eq("name", "ToDelete"),
	})
	assert.NoError(t, err)
	assert.Equal(t, int64(1), dbResult.RowsAffected)

	_, err = adapter.FindOne(ctx, "test_items", []limen.Where{limen.Eq("name", "ToDelete")}, nil)
	assert.ErrorIs(t, err, limen.ErrRecordNotFound)
}

func TestExists(t *testing.T) {
	t.Parallel()

	adapter := setupTestDB(t)
	ctx := context.Background()

	adapter.Create(ctx, "test_items", map[string]any{"name": "Exists"})

	exists, err := adapter.Exists(ctx, "test_items", []limen.Where{limen.Eq("name", "Exists")})
	assert.NoError(t, err)
	assert.True(t, exists)

	exists, err = adapter.Exists(ctx, "test_items", []limen.Where{limen.Eq("name", "Nope")})
	assert.NoError(t, err)
	assert.False(t, exists)
}

func TestCount(t *testing.T) {
	t.Parallel()

	adapter := setupTestDB(t)
	ctx := context.Background()

	for _, name := range []string{"A", "B", "C"} {
		adapter.Create(ctx, "test_items", map[string]any{"name": name})
	}

	count, err := adapter.Count(ctx, "test_items", nil)
	assert.NoError(t, err)
	assert.Equal(t, int64(3), count)

	count, err = adapter.Count(ctx, "test_items", []limen.Where{limen.Eq("name", "A")})
	assert.NoError(t, err)
	assert.Equal(t, int64(1), count)
}

func TestTransaction_Commit(t *testing.T) {
	t.Parallel()

	adapter := setupTestDB(t)
	ctx := context.Background()

	tx, err := adapter.BeginTx(ctx)
	assert.NoError(t, err)

	txAdapter := tx.(*Adapter)
	_, err = txAdapter.Create(ctx, "test_items", map[string]any{"name": "InTx"})
	assert.NoError(t, err)

	err = tx.Commit()
	assert.NoError(t, err)

	result, err := adapter.FindOne(ctx, "test_items", []limen.Where{limen.Eq("name", "InTx")}, nil)
	assert.NoError(t, err)
	assert.Equal(t, "InTx", result["name"])
}

func TestTransaction_Rollback(t *testing.T) {
	t.Parallel()

	adapter := setupTestDB(t)
	ctx := context.Background()

	tx, err := adapter.BeginTx(ctx)
	assert.NoError(t, err)

	txAdapter := tx.(*Adapter)
	txAdapter.Create(ctx, "test_items", map[string]any{"name": "Rolled"})

	err = tx.Rollback()
	assert.NoError(t, err)

	_, err = adapter.FindOne(ctx, "test_items", []limen.Where{limen.Eq("name", "Rolled")}, nil)
	assert.ErrorIs(t, err, limen.ErrRecordNotFound)
}

func TestWhereConditions(t *testing.T) {
	t.Parallel()

	adapter := setupTestDB(t)
	ctx := context.Background()

	for i, name := range []string{"Alice", "Bob", "Charlie"} {
		adapter.Create(ctx, "test_items", map[string]any{"name": name, "age": (i + 1) * 10})
	}

	tests := []struct {
		name       string
		conditions []limen.Where
		wantCount  int
	}{
		{name: "Eq", conditions: []limen.Where{limen.Eq("name", "Alice")}, wantCount: 1},
		{name: "Ne", conditions: []limen.Where{limen.Ne("name", "Alice")}, wantCount: 2},
		{name: "Lt", conditions: []limen.Where{limen.Lt("age", 15)}, wantCount: 1},
		{name: "Gt", conditions: []limen.Where{limen.Gt("age", 10)}, wantCount: 2},
		{name: "In", conditions: []limen.Where{limen.In("name", []any{"Alice", "Charlie"})}, wantCount: 2},
		{name: "Contains", conditions: []limen.Where{limen.Contains("name", "li")}, wantCount: 2}, // Alice + Charlie
		{name: "IsNull", conditions: []limen.Where{limen.IsNull("email")}, wantCount: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			results, err := adapter.FindMany(ctx, "test_items", tt.conditions, nil)
			assert.NoError(t, err)
			assert.Len(t, results, tt.wantCount)
		})
	}
}

func TestUpdate_RequiresConditions(t *testing.T) {
	t.Parallel()

	adapter := setupTestDB(t)
	ctx := context.Background()

	_, err := adapter.Update(ctx, "test_items", nil, map[string]any{"name": "bad"})
	assert.Error(t, err)
}

func TestDelete_RequiresConditions(t *testing.T) {
	t.Parallel()

	adapter := setupTestDB(t)
	ctx := context.Background()

	_, err := adapter.Delete(ctx, "test_items", nil)
	assert.Error(t, err)
}

func TestRelatedPageAndCount(t *testing.T) {
	adapter := setupTestDB(t)
	ctx := context.Background()
	// Count and page must use the same bound relation filter.
	for _, item := range []map[string]any{{"name": "relation-first", "email": "safe", "age": 11}, {"name": "relation-second", "email": "safe", "age": 12}, {"name": "unrelated", "email": "foreign", "age": 13}} {
		_, err := adapter.Create(ctx, "test_items", item)
		require.NoError(t, err)
	}
	relation := limen.RelatedQuery{Table: "test_items", Column: "id", Conditions: []limen.Where{limen.Eq("email", "safe")}}
	total, err := adapter.CountRelated(ctx, "test_items", "id", nil, relation)
	require.NoError(t, err)
	require.EqualValues(t, 2, total)
	rows, err := adapter.FindRelated(ctx, "test_items", "id", nil, relation, &limen.QueryOptions{Limit: 1, Offset: 1, OrderBy: []limen.OrderBy{{Column: "age", Direction: limen.OrderByAsc}}})
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, "relation-second", rows[0]["name"])
	relation.Conditions = []limen.Where{limen.Eq("email", "safe' OR 1=1 --")}
	total, err = adapter.CountRelated(ctx, "test_items", "id", nil, relation)
	require.NoError(t, err)
	require.Zero(t, total)
	_, err = adapter.FindRelated(ctx, "test_items", "", nil, relation, nil)
	require.Error(t, err)
}
