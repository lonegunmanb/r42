package progress

import (
	"fmt"
	"maps"
	"reflect"
	"slices"
)

type schemaDefinition struct {
	encode schemaRecordEncoder
}

var schemaDefinitions = map[int]schemaDefinition{
	SchemaMajor1: {encode: schema1WireRecord},
}

// AdvertisedSchemaMajors returns every event schema major this build can
// encode, in ascending order.
func AdvertisedSchemaMajors() []int {
	majors := slices.Collect(maps.Keys(schemaDefinitions))
	slices.Sort(majors)
	return majors
}

func supportedSchemaMajor(major int) bool {
	_, ok := schemaDefinitions[major]
	return ok
}

func schemaDefinitionFor(major int) (schemaDefinition, error) {
	if err := validateSchemaDefinitions(schemaDefinitions); err != nil {
		return schemaDefinition{}, err
	}
	definition, ok := schemaDefinitions[major]
	if !ok {
		return schemaDefinition{}, fmt.Errorf("unsupported schema major %d", major)
	}
	return definition, nil
}

// validateSchemaDefinitions makes schema-major evolution explicit: each
// advertised major has its own encoder, and adding a major retains the
// immediately preceding major for worker compatibility.
func validateSchemaDefinitions(definitions map[int]schemaDefinition) error {
	if len(definitions) == 0 {
		return fmt.Errorf("schema registry must not be empty")
	}
	encoders := make(map[uintptr]int, len(definitions))
	for major, definition := range definitions {
		if major < SchemaMajor1 {
			return fmt.Errorf("invalid schema major %d", major)
		}
		if definition.encode == nil {
			return fmt.Errorf("schema major %d has no encoder", major)
		}
		pointer := reflect.ValueOf(definition.encode).Pointer()
		if previous, exists := encoders[pointer]; exists {
			return fmt.Errorf("schema majors %d and %d must use distinct encoders", previous, major)
		}
		encoders[pointer] = major
		if major > SchemaMajor1 {
			if _, ok := definitions[major-1]; !ok {
				return fmt.Errorf("schema major %d requires previous major %d", major, major-1)
			}
		}
	}
	return nil
}
