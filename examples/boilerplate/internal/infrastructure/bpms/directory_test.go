package bpms

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryDirectory(t *testing.T) {
	ctx := context.Background()
	d := NewMemoryDirectory(
		ProfileHolder{Position: "encargado", Unit: "atencion", UserID: "luis"},
		ProfileHolder{Position: "encargado", Unit: "atencion", UserID: "ana"},
	)

	got, err := d.Holders(ctx, "encargado", "atencion")
	require.NoError(t, err)
	assert.Equal(t, []string{"luis", "ana"}, got)

	d.Set("encargado", "atencion", "maria")
	got, err = d.Holders(ctx, "encargado", "atencion")
	require.NoError(t, err)
	assert.Equal(t, []string{"maria"}, got)

	got, err = d.Holders(ctx, "jefe", "atencion")
	require.NoError(t, err)
	assert.Empty(t, got)
}
