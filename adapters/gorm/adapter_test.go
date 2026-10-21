package gorm

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/thecodearcher/limen"
)

func setupTestGormDB(t *testing.T) *Adapter {
	t.Helper()

	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{
		Logger: logger.Discard,
	})
	if err != nil {
		t.Fatalf("failed to open gorm sqlite: %v", err)
	}

	sqlDB, _ := db.DB()
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { sqlDB.Close() })

	db.Exec(`CREATE TABLE IF NOT EXISTS "test_items" (
		"id" INTEGER PRIMARY KEY AUTOINCREMENT,
		"name" TEXT NOT NULL,
		"email" TEXT,
		"age" INTEGER DEFAULT 0,
		"status" TEXT DEFAULT 'active'
	)`)

	return New(db)
}

func TestGorm_Create_And_FindOne(t *testing.T) {
	adapter := setupTestGormDB(t)
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

func TestGorm_FindOne_NotFound(t *testing.T) {
	adapter := setupTestGormDB(t)
	ctx := context.Background()

	_, err := adapter.FindOne(ctx, "test_items", []limen.Where{
		limen.Eq("name", "Nonexistent"),
	}, nil)
	assert.ErrorIs(t, err, limen.ErrRecordNotFound)
}

func TestGorm_FindMany(t *testing.T) {
	adapter := setupTestGormDB(t)
	ctx := context.Background()

	for _, name := range []string{"Alice", "Bob", "Charlie"} {
		adapter.Create(ctx, "test_items", map[string]any{"name": name})
	}

	results, err := adapter.FindMany(ctx, "test_items", nil, nil)
	assert.NoError(t, err)
	assert.Len(t, results, 3)
}

func TestGorm_Update(t *testing.T) {
	adapter := setupTestGormDB(t)
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

func TestGorm_Update_MixesArithmeticAndAssignments(t *testing.T) {
	adapter := setupTestGormDB(t)
	ctx := t.Context()
	_, err := adapter.Create(ctx, "test_items", map[string]any{
		"name":   "Arithmetic",
		"age":    10,
		"status": "active",
	})
	require.NoError(t, err)

	_, err = adapter.Update(ctx, "test_items", []limen.Where{
		limen.Eq("name", "Arithmetic"),
	}, map[string]any{
		"age":    limen.IncrementBy(5),
		"status": "updated",
	})
	require.NoError(t, err)

	_, err = adapter.Update(ctx, "test_items", []limen.Where{
		limen.Eq("name", "Arithmetic"),
	}, map[string]any{
		"age": limen.DecrementBy(3),
	})
	require.NoError(t, err)

	result, err := adapter.FindOne(ctx, "test_items", []limen.Where{
		limen.Eq("name", "Arithmetic"),
	}, nil)
	require.NoError(t, err)
	assert.EqualValues(t, 12, result["age"])
	assert.Equal(t, "updated", result["status"])
}

func TestGorm_Update_ConcurrentIncrements(t *testing.T) {
	adapter := setupTestGormDB(t)
	ctx := t.Context()
	_, err := adapter.Create(ctx, "test_items", map[string]any{
		"name": "GormCounter",
		"age":  0,
	})
	require.NoError(t, err)

	const increments = 50
	var wg sync.WaitGroup
	errs := make(chan error, increments)
	for range increments {
		wg.Go(func() {
			_, err := adapter.Update(ctx, "test_items", []limen.Where{
				limen.Eq("name", "GormCounter"),
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
		limen.Eq("name", "GormCounter"),
	}, nil)
	require.NoError(t, err)
	assert.EqualValues(t, increments, result["age"])
}

func TestGorm_Delete(t *testing.T) {
	adapter := setupTestGormDB(t)
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

func TestGorm_Exists(t *testing.T) {
	adapter := setupTestGormDB(t)
	ctx := context.Background()

	adapter.Create(ctx, "test_items", map[string]any{"name": "Exists"})

	exists, err := adapter.Exists(ctx, "test_items", []limen.Where{limen.Eq("name", "Exists")})
	assert.NoError(t, err)
	assert.True(t, exists)

	exists, err = adapter.Exists(ctx, "test_items", []limen.Where{limen.Eq("name", "Nope")})
	assert.NoError(t, err)
	assert.False(t, exists)
}

func TestGorm_Count(t *testing.T) {
	adapter := setupTestGormDB(t)
	ctx := context.Background()

	for _, name := range []string{"A", "B", "C"} {
		adapter.Create(ctx, "test_items", map[string]any{"name": name})
	}

	count, err := adapter.Count(ctx, "test_items", nil)
	assert.NoError(t, err)
	assert.Equal(t, int64(3), count)
}

func TestGorm_Transaction_Commit(t *testing.T) {
	adapter := setupTestGormDB(t)
	ctx := context.Background()

	tx, err := adapter.BeginTx(ctx)
	assert.NoError(t, err)

	txAdapter := tx.(*Adapter)
	txAdapter.Create(ctx, "test_items", map[string]any{"name": "InTx"})

	err = tx.Commit()
	assert.NoError(t, err)

	result, err := adapter.FindOne(ctx, "test_items", []limen.Where{limen.Eq("name", "InTx")}, nil)
	assert.NoError(t, err)
	assert.Equal(t, "InTx", result["name"])
}

func TestGorm_Transaction_Rollback(t *testing.T) {
	adapter := setupTestGormDB(t)
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

func TestRelatedPageAndCount(t *testing.T) {
	adapter := setupTestGormDB(t)
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
