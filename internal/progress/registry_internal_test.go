package progress

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateSchemaDefinitions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		definitions map[int]schemaDefinition
		wantErr     string
	}{
		{
			name: "adjacent major encoders are distinct",
			definitions: map[int]schemaDefinition{
				SchemaMajor1:     {encode: testSchemaEncoderOne},
				SchemaMajor1 + 1: {encode: testSchemaEncoderTwo},
			},
		},
		{
			name: "missing encoder",
			definitions: map[int]schemaDefinition{
				SchemaMajor1: {},
			},
			wantErr: "encoder",
		},
		{
			name: "duplicate encoder",
			definitions: map[int]schemaDefinition{
				SchemaMajor1:     {encode: testSchemaEncoderOne},
				SchemaMajor1 + 1: {encode: testSchemaEncoderOne},
			},
			wantErr: "distinct",
		},
		{
			name: "skips immediately previous major",
			definitions: map[int]schemaDefinition{
				SchemaMajor1:     {encode: testSchemaEncoderOne},
				SchemaMajor1 + 2: {encode: testSchemaEncoderTwo},
			},
			wantErr: "previous",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := validateSchemaDefinitions(test.definitions)
			if test.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.ErrorContains(t, err, test.wantErr)
		})
	}
}

func testSchemaEncoderOne(Record) (any, bool) { return nil, false }

func testSchemaEncoderTwo(Record) (any, bool) { return nil, false }
