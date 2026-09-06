package request_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/kafeiih/vogel/request"
)

func TestValidator_HasErrorsAndErrors(t *testing.T) {
	v := request.NewValidator()
	assert.False(t, v.HasErrors())
	assert.Empty(t, v.Errors())

	r := httptest.NewRequest("GET", "/?limit=notanumber", nil)
	v.IntQuery(r, "limit", 10)

	assert.True(t, v.HasErrors())
	assert.Contains(t, v.Errors(), "limit")
}

func TestValidator_UUIDParam(t *testing.T) {
	t.Run("valid uuid", func(t *testing.T) {
		id := uuid.New()
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", id.String())
		r := httptest.NewRequest("GET", "/", nil)
		r = requestWithRouteContext(r, rctx)

		v := request.NewValidator()
		got := v.UUIDParam(r, "id")
		assert.False(t, v.HasErrors())
		assert.Equal(t, id, got)
	})

	t.Run("missing param", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		v := request.NewValidator()
		got := v.UUIDParam(r, "id")
		assert.True(t, v.HasErrors())
		assert.Equal(t, uuid.Nil, got)
		assert.Equal(t, "id is required", v.Errors()["id"])
	})

	t.Run("invalid uuid", func(t *testing.T) {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", "not-a-uuid")
		r := httptest.NewRequest("GET", "/", nil)
		r = requestWithRouteContext(r, rctx)

		v := request.NewValidator()
		got := v.UUIDParam(r, "id")
		assert.True(t, v.HasErrors())
		assert.Equal(t, uuid.Nil, got)
	})
}

func TestValidator_IntQuery(t *testing.T) {
	t.Run("absent returns default", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		v := request.NewValidator()
		assert.Equal(t, 20, v.IntQuery(r, "limit", 20))
		assert.False(t, v.HasErrors())
	})

	t.Run("valid value", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/?limit=50", nil)
		v := request.NewValidator()
		assert.Equal(t, 50, v.IntQuery(r, "limit", 20))
		assert.False(t, v.HasErrors())
	})

	t.Run("negative value is rejected", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/?limit=-1", nil)
		v := request.NewValidator()
		got := v.IntQuery(r, "limit", 20)
		assert.Equal(t, 20, got, "default is returned on error")
		assert.True(t, v.HasErrors())
	})

	t.Run("non-integer value is rejected", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/?limit=abc", nil)
		v := request.NewValidator()
		got := v.IntQuery(r, "limit", 20)
		assert.Equal(t, 20, got)
		assert.True(t, v.HasErrors())
	})
}

func TestValidator_MaxInt(t *testing.T) {
	v := request.NewValidator()
	assert.Equal(t, 100, v.MaxInt("limit", 150, 100))
	assert.Equal(t, 50, v.MaxInt("limit", 50, 100))
}

func TestValidator_TimeQuery(t *testing.T) {
	t.Run("absent returns nil", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		v := request.NewValidator()
		assert.Nil(t, v.TimeQuery(r, "since"))
		assert.False(t, v.HasErrors())
	})

	t.Run("valid RFC3339", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/?since=2024-01-15T10:00:00Z", nil)
		v := request.NewValidator()
		got := v.TimeQuery(r, "since")
		require.NotNil(t, got)
		assert.Equal(t, 2024, got.Year())
	})

	t.Run("invalid format", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/?since=not-a-time", nil)
		v := request.NewValidator()
		assert.Nil(t, v.TimeQuery(r, "since"))
		assert.True(t, v.HasErrors())
	})
}

func TestValidator_DateQuery(t *testing.T) {
	t.Run("valid ISO date", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/?date=2024-01-15", nil)
		v := request.NewValidator()
		got := v.DateQuery(r, "date")
		require.NotNil(t, got)
		assert.Equal(t, 15, got.Day())
	})

	t.Run("invalid format", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/?date=15/01/2024", nil)
		v := request.NewValidator()
		assert.Nil(t, v.DateQuery(r, "date"))
		assert.True(t, v.HasErrors())
	})
}

func TestValidator_UUIDQuery(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		id := uuid.New()
		r := httptest.NewRequest("GET", "/?id="+id.String(), nil)
		v := request.NewValidator()
		got := v.UUIDQuery(r, "id")
		require.NotNil(t, got)
		assert.Equal(t, id, *got)
	})

	t.Run("invalid", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/?id=not-a-uuid", nil)
		v := request.NewValidator()
		assert.Nil(t, v.UUIDQuery(r, "id"))
		assert.True(t, v.HasErrors())
	})
}

func TestValidator_Int64Param(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", "12345678901")
		r := httptest.NewRequest("GET", "/", nil)
		r = requestWithRouteContext(r, rctx)

		v := request.NewValidator()
		assert.Equal(t, int64(12345678901), v.Int64Param(r, "id"))
		assert.False(t, v.HasErrors())
	})

	t.Run("missing", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		v := request.NewValidator()
		assert.Equal(t, int64(0), v.Int64Param(r, "id"))
		assert.True(t, v.HasErrors())
	})
}

func TestValidator_Enum(t *testing.T) {
	t.Run("empty is allowed", func(t *testing.T) {
		v := request.NewValidator()
		assert.Equal(t, "", v.Enum("status", "", []string{"active", "inactive"}))
		assert.False(t, v.HasErrors())
	})

	t.Run("matching value", func(t *testing.T) {
		v := request.NewValidator()
		assert.Equal(t, "active", v.Enum("status", "active", []string{"active", "inactive"}))
		assert.False(t, v.HasErrors())
	})

	t.Run("non-matching value", func(t *testing.T) {
		v := request.NewValidator()
		v.Enum("status", "bogus", []string{"active", "inactive"})
		assert.True(t, v.HasErrors())
	})
}

func TestValidator_PublicIDParam(t *testing.T) {
	t.Run("valid uuid", func(t *testing.T) {
		id := uuid.New()
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", id.String())
		r := httptest.NewRequest("GET", "/", nil)
		r = requestWithRouteContext(r, rctx)

		v := request.NewValidator()
		assert.Equal(t, id.String(), v.PublicIDParam(r, "id"))
		assert.False(t, v.HasErrors())
	})

	t.Run("valid ulid", func(t *testing.T) {
		ulid := "01ARZ3NDEKTSV4RRFFQ69G5FAV"
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", ulid)
		r := httptest.NewRequest("GET", "/", nil)
		r = requestWithRouteContext(r, rctx)

		v := request.NewValidator()
		assert.Equal(t, ulid, v.PublicIDParam(r, "id"))
		assert.False(t, v.HasErrors())
	})

	t.Run("invalid", func(t *testing.T) {
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("id", "not-valid")
		r := httptest.NewRequest("GET", "/", nil)
		r = requestWithRouteContext(r, rctx)

		v := request.NewValidator()
		assert.Equal(t, "", v.PublicIDParam(r, "id"))
		assert.True(t, v.HasErrors())
	})
}

func TestValidator_Int64sQuery(t *testing.T) {
	t.Run("absent returns nil", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/", nil)
		v := request.NewValidator()
		assert.Nil(t, v.Int64sQuery(r, "ids"))
		assert.False(t, v.HasErrors())
	})

	t.Run("valid list", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/?ids=1,2,3", nil)
		v := request.NewValidator()
		got := v.Int64sQuery(r, "ids")
		assert.Equal(t, []int64{1, 2, 3}, got)
		assert.False(t, v.HasErrors())
	})

	t.Run("invalid entry", func(t *testing.T) {
		r := httptest.NewRequest("GET", "/?ids=1,x,3", nil)
		v := request.NewValidator()
		got := v.Int64sQuery(r, "ids")
		assert.Nil(t, got)
		assert.True(t, v.HasErrors())
	})
}

func TestValidator_WriteErrors(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()

	v := request.NewValidator()
	v.Enum("status", "bogus", []string{"active"})
	v.WriteErrors(w, r)

	assert.Equal(t, 400, w.Code)
	assert.Contains(t, w.Body.String(), "status")
}

// requestWithRouteContext attaches a chi RouteContext to r so chi.URLParam can
// resolve URL parameters outside of an actual chi router.
func requestWithRouteContext(r *http.Request, rctx *chi.Context) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
}
