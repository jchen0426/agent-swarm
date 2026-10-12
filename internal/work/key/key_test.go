package key_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/jchen0426/agent-swarm/internal/work/key"
)

func incidentSchema() key.Schema {
	return key.Schema{Fields: []key.Field{
		{Name: "service", Values: []string{"checkout", "payments"}},
		{Name: "failureMode", Values: []string{"bad-deploy", "config-change"}},
	}}
}

func TestValidate(t *testing.T) {
	tests := []struct {
		name string
		k    key.Key
		want error
	}{
		{"valid", key.Key{"service": "checkout", "failureMode": "bad-deploy"}, nil},
		{"other is always allowed", key.Key{"service": key.Other, "failureMode": key.Other}, nil},
		{"missing field", key.Key{"service": "checkout"}, key.ErrMissingField},
		{"unknown field", key.Key{"service": "checkout", "failureMode": "bad-deploy", "region": "eu"}, key.ErrUnknownField},
		{"value outside the set", key.Key{"service": "search", "failureMode": "bad-deploy"}, key.ErrValue},
		{"values are case sensitive", key.Key{"service": "Checkout", "failureMode": "bad-deploy"}, key.ErrValue},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := incidentSchema().Validate(tt.k)
			if tt.want == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, tt.want)
		})
	}
}

func TestEmptyClosedSetAcceptsOnlyOther(t *testing.T) {
	s := key.Schema{Fields: []key.Field{{Name: "failureMode"}}}

	require.NoError(t, s.Validate(key.Key{"failureMode": key.Other}))
	require.ErrorIs(t, s.Validate(key.Key{"failureMode": "bad-deploy"}), key.ErrValue)
}

func TestMergeable(t *testing.T) {
	s := incidentSchema()

	assert.True(t, s.Mergeable(key.Key{"service": "checkout", "failureMode": "bad-deploy"}))
	assert.False(t, s.Mergeable(key.Key{"service": "checkout", "failureMode": key.Other}))
	assert.False(t, key.Schema{}.Mergeable(key.Key{}), "a schema without fields never merges")
}

func TestEqualAndString(t *testing.T) {
	a := key.Key{"service": "checkout", "failureMode": "bad-deploy"}
	b := key.Key{"failureMode": "bad-deploy", "service": "checkout"}

	assert.True(t, a.Equal(b))
	assert.Equal(t, `failureMode="bad-deploy",service="checkout"`, a.String())
	assert.Equal(t, a.String(), b.String())
	assert.False(t, a.Equal(key.Key{"service": "checkout", "failureMode": "config-change"}))
	assert.NotEqual(t, key.Key{"service": "a,b"}.String(), key.Key{"service": "a", "b": ""}.String())
}
