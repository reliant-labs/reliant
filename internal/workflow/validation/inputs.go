// Copyright (c) 2025 Reliant Labs
package validation

import (
	"fmt"
	"math"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"

	reliantv1 "github.com/reliant-labs/reliant/gen/reliant/v1"
	"github.com/reliant-labs/reliant/internal/workflow/model"
	"google.golang.org/protobuf/types/known/structpb"
)

// validateBoundInputs validates actual input values against the workflow's proto input schema.
func validateBoundInputs(wf *reliantv1.Workflow, inputs map[string]any, vt ValueTypes, result *Result) {
	wfInputs := wf.GetInputs()
	if len(wfInputs) == 0 {
		return
	}

	path := []string{wf.GetName(), "inputs"}

	// Reject flat input keys (e.g., "agent.model") - inputs must be properly nested
	var flatKeys []string
	for key := range inputs {
		if strings.Contains(key, ".") {
			flatKeys = append(flatKeys, key)
		}
	}
	if len(flatKeys) > 0 {
		sort.Strings(flatKeys)
		result.AddError(CategoryInput, path, "",
			fmt.Sprintf("flat input keys are not allowed, use nested structure instead: %s", strings.Join(flatKeys, ", ")))
		return
	}

	// Check each declared input
	for name, inputDef := range wfInputs {
		if inputDef == nil {
			continue
		}

		inputPath := append(path, name)

		// Handle group inputs by validating nested inputs recursively
		if model.IsGroupInput(inputDef) {
			validateProtoGroupInput(name, inputDef, inputs, inputPath, vt, result)
			continue
		}

		providedValue, provided := inputs[name]

		// Check required inputs are provided
		if model.IsInputRequired(inputDef) && (!provided || providedValue == nil) {
			result.AddError(CategoryInput, inputPath, "",
				fmt.Sprintf("required input '%s' is not provided", name))
			continue
		}

		// If not provided, skip further validation
		if !provided || providedValue == nil {
			continue
		}

		// Type + constraint validation
		if err := validateProtoInputType(name, providedValue, inputDef, vt); err != nil {
			result.AddError(CategoryInput, inputPath, "", err.Error())
		}
	}

	// Check for unknown inputs
	var unknownInputs []string
	for key := range inputs {
		if _, exists := wfInputs[key]; !exists {
			unknownInputs = append(unknownInputs, key)
		}
	}

	if len(unknownInputs) > 0 {
		sort.Strings(unknownInputs)

		var validInputs []string
		for name, input := range wfInputs {
			if input != nil && !model.IsGroupInput(input) {
				validInputs = append(validInputs, name)
			}
		}
		sort.Strings(validInputs)

		suggestion := ""
		if len(unknownInputs) == 1 {
			suggestion = suggestSimilar(unknownInputs[0], validInputs)
		}

		if suggestion != "" {
			result.AddErrorWithSuggestion(CategoryInput, path, "",
				fmt.Sprintf("unknown input(s): %s", strings.Join(unknownInputs, ", ")),
				suggestion)
		} else {
			result.AddError(CategoryInput, path, "",
				fmt.Sprintf("unknown input(s): %s", strings.Join(unknownInputs, ", ")))
		}
	}
}

// validateProtoInputType validates that a provided value matches the expected proto input type.
func validateProtoInputType(name string, value any, inputDef *reliantv1.Input, vt ValueTypes) error {
	if value == nil {
		return nil
	}

	if isIntegrationInput(inputDef) {
		return validateIntegrationInputValue(name, value, inputDef, vt)
	}

	expectedType := inputDef.GetType()
	switch expectedType {
	case "string", "message":
		stringValue, ok := textValue(value)
		if !ok {
			return fmt.Errorf("input '%s' expects string, got %T", name, value)
		}
		if cfg := inputDef.GetStringInput(); cfg != nil {
			if err := validateStringConstraints(name, stringValue, cfg.GetPattern(), cfg.MinLength, cfg.MaxLength); err != nil {
				return err
			}
		}
		if cfg := inputDef.GetMessageInput(); cfg != nil {
			_ = cfg // message currently has no additional constraints beyond string type.
		}
		return nil

	case "enum":
		return validateEnumInputType(name, value, inputDef.GetEnumInput())

	case "model":
		switch modelValue := value.(type) {
		case map[string]interface{}:
			if _, hasID := modelValue["id"]; !hasID {
				if _, hasTags := modelValue["tags"]; !hasTags {
					return fmt.Errorf("input '%s' model selector expects 'id' or 'tags'", name)
				}
			}
		case string:
			return fmt.Errorf("input '%s' model must be an object (e.g. {id: \"model-name\"}), got string %q — convert to {id: string} at the system boundary", name, modelValue)
		default:
			return fmt.Errorf("input '%s' expects model selector object, got %T", name, value)
		}
		return nil

	case "integer":
		integerValue, ok := coerceInteger(value)
		if !ok {
			return typeMismatch(name, "integer", value)
		}
		if cfg := inputDef.GetIntegerInput(); cfg != nil {
			if cfg.Min != nil && integerValue < cfg.GetMin() {
				return fmt.Errorf("input '%s' must be >= %d", name, cfg.GetMin())
			}
			if cfg.Max != nil && integerValue > cfg.GetMax() {
				return fmt.Errorf("input '%s' must be <= %d", name, cfg.GetMax())
			}
		}
		return nil

	case "number":
		numberValue, ok := coerceNumber(value)
		if !ok {
			return typeMismatch(name, "number", value)
		}
		if cfg := inputDef.GetNumberInput(); cfg != nil {
			if cfg.Min != nil && numberValue < cfg.GetMin() {
				return fmt.Errorf("input '%s' must be >= %v", name, cfg.GetMin())
			}
			if cfg.Max != nil && numberValue > cfg.GetMax() {
				return fmt.Errorf("input '%s' must be <= %v", name, cfg.GetMax())
			}
		}
		return nil

	case "boolean":
		if _, ok := value.(bool); !ok {
			return typeMismatch(name, "boolean", value)
		}
		return nil

	case "array":
		arrayValue, ok := toAnySlice(value)
		if !ok {
			return fmt.Errorf("input '%s' expects array, got %T", name, value)
		}
		if cfg := inputDef.GetArrayInput(); cfg != nil {
			if cfg.MinItems != nil && len(arrayValue) < int(cfg.GetMinItems()) {
				return fmt.Errorf("input '%s' requires at least %d item(s)", name, cfg.GetMinItems())
			}
			if cfg.MaxItems != nil && len(arrayValue) > int(cfg.GetMaxItems()) {
				return fmt.Errorf("input '%s' allows at most %d item(s)", name, cfg.GetMaxItems())
			}
		}
		return nil

	case "attachments":
		arrayValue, ok := toAnySlice(value)
		if !ok {
			return fmt.Errorf("input '%s' expects array, got %T", name, value)
		}
		if cfg := inputDef.GetAttachmentsInput(); cfg != nil {
			if cfg.MinItems != nil && len(arrayValue) < int(cfg.GetMinItems()) {
				return fmt.Errorf("input '%s' requires at least %d item(s)", name, cfg.GetMinItems())
			}
			if cfg.MaxItems != nil && len(arrayValue) > int(cfg.GetMaxItems()) {
				return fmt.Errorf("input '%s' allows at most %d item(s)", name, cfg.GetMaxItems())
			}
		}
		return nil

	case "tools":
		arrayValue, ok := toAnySlice(value)
		if !ok {
			return fmt.Errorf("input '%s' expects array, got %T", name, value)
		}
		for index, item := range arrayValue {
			if _, stringOK := item.(string); !stringOK {
				return fmt.Errorf("input '%s' expects array of strings, item %d is %T", name, index, item)
			}
		}
		return nil

	case "object":
		objectValue, ok := toStringAnyMap(value)
		if !ok {
			return fmt.Errorf("input '%s' expects object, got %T", name, value)
		}
		if cfg := inputDef.GetObjectInput(); cfg != nil {
			if err := validateObjectInputConstraints(name, objectValue, cfg); err != nil {
				return err
			}
		}
		return nil

	case "any":
		return nil

	case "preset":
		return validatePresetInputType(name, value, inputDef.GetPresetInput())
	}

	return nil
}

func validateStringConstraints(name, value, pattern string, minLength, maxLength *int32) error {
	if minLength != nil && len(value) < int(*minLength) {
		return fmt.Errorf("input '%s' must be at least %d character(s)", name, *minLength)
	}
	if maxLength != nil && len(value) > int(*maxLength) {
		return fmt.Errorf("input '%s' must be at most %d character(s)", name, *maxLength)
	}
	if pattern != "" {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return fmt.Errorf("input '%s' has invalid pattern constraint: %v", name, err)
		}
		if !re.MatchString(value) {
			return fmt.Errorf("input '%s' must match pattern %q", name, pattern)
		}
	}
	return nil
}

func validateEnumInputType(name string, value any, enumConfig *reliantv1.EnumInputConfig) error {
	if enumConfig == nil {
		if _, ok := value.(string); !ok {
			return fmt.Errorf("input '%s' expects string, got %T", name, value)
		}
		return nil
	}

	// An enum with no declared values constrains only the shape (string /
	// array of strings), never the content — otherwise every value is
	// rejected with an empty "(allowed: )" list, which bricks in-flight
	// workflows whenever a schema is loaded mid-edit without its values.
	allowedValues := enumConfig.GetEnumValues()
	unrestricted := len(allowedValues) == 0
	allowedValueSet := make(map[string]struct{}, len(allowedValues))
	for _, allowedValue := range allowedValues {
		allowedValueSet[allowedValue] = struct{}{}
	}

	if enumConfig.GetMulti() {
		enumSlice, ok := toAnySlice(value)
		if !ok {
			return fmt.Errorf("input '%s' expects array of enum values, got %T", name, value)
		}
		for index, enumValue := range enumSlice {
			enumString, stringOK := enumValue.(string)
			if !stringOK {
				return fmt.Errorf("input '%s' expects array of strings, item %d is %T", name, index, enumValue)
			}
			if _, allowed := allowedValueSet[enumString]; !allowed && !unrestricted {
				return fmt.Errorf("input '%s' has invalid enum value %q (allowed: %s)", name, enumString, strings.Join(allowedValues, ", "))
			}
		}
		return nil
	}

	enumString, ok := textValue(value)
	if !ok {
		return fmt.Errorf("input '%s' expects string, got %T", name, value)
	}
	if _, allowed := allowedValueSet[enumString]; !allowed && !unrestricted {
		return fmt.Errorf("input '%s' has invalid enum value %q (allowed: %s)", name, enumString, strings.Join(allowedValues, ", "))
	}
	return nil
}

// InputValue is the one rule for what a value of an input's declared type
// may be, and the form a run holds it in. Admission (launch), the runtime's
// own input check and `reliant workflow validate` all apply it, so a value
// one accepts the others accept:
//
//   - boolean: a bool. Never text: "true" (a quoted 'true' in YAML, a JSON
//     string) is refused rather than read as a boolean. YAML and JSON both
//     have real booleans, so text where a boolean is declared is a mistake to
//     report, not a meaning to guess.
//   - integer: a number with no fractional part, held as int64. number: any
//     number, held as float64. Never text, for the same reason.
//   - string, message, single enum: text only. A number is not text either:
//     a template yielding a PR number fills a string input through
//     {{ string(...) }}, so what the run holds is what the author wrote.
//   - every other type is held as given (the type check proper is in
//     validateProtoInputType).
//
// ok is false when value does not fit the type; the value is then returned
// unchanged, for the check to report.
func InputValue(value any, input *reliantv1.Input) (any, bool) {
	if input == nil || value == nil {
		return value, false
	}
	if isIntegrationInput(input) {
		return value, true
	}
	if enumCfg := input.GetEnumInput(); enumCfg != nil && enumCfg.GetMulti() {
		items, ok := toAnySlice(value)
		if !ok {
			return value, false
		}
		return items, true
	}
	switch model.GetInputType(input) {
	case "boolean":
		b, ok := value.(bool)
		if !ok {
			return value, false
		}
		return b, true
	case "integer":
		i, ok := coerceInteger(value)
		if !ok {
			return value, false
		}
		return i, true
	case "number":
		f, ok := coerceNumber(value)
		if !ok {
			return value, false
		}
		return f, true
	case "string", "message", "enum":
		s, ok := textValue(value)
		if !ok {
			return value, false
		}
		return s, true
	}
	return value, false
}

// textValue is value when it is text.
func textValue(value any) (string, bool) {
	s, ok := value.(string)
	return s, ok
}

// typeMismatch is the error for a value of the wrong type for a boolean,
// integer or number input. Text that spells such a value gets the fix named:
// it is almost always a quoted literal ('true', "10") where the bare one was
// meant.
func typeMismatch(name, want string, value any) error {
	text, isText := value.(string)
	if !isText {
		return fmt.Errorf("input '%s' expects %s, got %T", name, want, value)
	}
	if spellsType(want, text) {
		return fmt.Errorf("input '%s' expects %s, got string %q: text is never read as a %s; write %s without quotes, or an expression that yields a %s",
			name, want, text, want, strings.TrimSpace(text), want)
	}
	return fmt.Errorf("input '%s' expects %s, got string %q", name, want, text)
}

// spellsType reports whether text is how a value of type want is written.
func spellsType(want, text string) bool {
	text = strings.TrimSpace(text)
	switch want {
	case "boolean":
		return text == "true" || text == "false"
	case "integer":
		_, err := strconv.ParseInt(text, 10, 64)
		return err == nil
	case "number":
		_, err := strconv.ParseFloat(text, 64)
		return err == nil
	}
	return false
}

func validatePresetInputType(name string, value any, presetConfig *reliantv1.PresetInputConfig) error {
	if presetConfig != nil && presetConfig.GetMulti() {
		presetSlice, ok := toAnySlice(value)
		if !ok {
			return fmt.Errorf("input '%s' expects array of preset slugs, got %T", name, value)
		}
		for index, presetValue := range presetSlice {
			if _, ok := presetValue.(string); !ok {
				return fmt.Errorf("input '%s' expects array of strings, item %d is %T", name, index, presetValue)
			}
		}
		return nil
	}

	if _, ok := value.(string); !ok {
		return fmt.Errorf("input '%s' expects string, got %T", name, value)
	}
	return nil
}

func validateObjectInputConstraints(name string, objectValue map[string]any, objectConfig *reliantv1.ObjectInputConfig) error {
	for _, requiredPropertyName := range objectConfig.GetRequired() {
		if _, ok := objectValue[requiredPropertyName]; !ok {
			return fmt.Errorf("input '%s' missing required property %q", name, requiredPropertyName)
		}
	}

	if objectConfig.AdditionalProperties != nil && !objectConfig.GetAdditionalProperties() {
		for propertyName := range objectValue {
			if _, ok := objectConfig.GetProperties()[propertyName]; !ok {
				return fmt.Errorf("input '%s' has unknown property %q", name, propertyName)
			}
		}
	}

	for propertyName, propertyValue := range objectValue {
		propertySchema, hasSchema := objectConfig.GetProperties()[propertyName]
		if !hasSchema || propertySchema == nil {
			continue
		}
		propertyPath := fmt.Sprintf("%s.%s", name, propertyName)
		if err := validatePropertySchemaValue(propertyPath, propertyValue, propertySchema); err != nil {
			return err
		}
	}

	return nil
}

func validatePropertySchemaValue(path string, value any, propertySchema *reliantv1.PropertySchema) error {
	if propertySchema == nil {
		return nil
	}

	if len(propertySchema.GetEnumValues()) > 0 && !valueInPropertyEnum(value, propertySchema.GetEnumValues()) {
		return fmt.Errorf("input '%s' has value %v not present in enum", path, value)
	}

	switch propertySchema.GetType() {
	case "", "any":
		return nil
	case "string":
		stringValue, ok := value.(string)
		if !ok {
			return fmt.Errorf("input '%s' expects string, got %T", path, value)
		}
		return validateStringConstraints(path, stringValue, "", propertySchema.MinLength, propertySchema.MaxLength)
	case "number":
		numberValue, ok := coerceNumber(value)
		if !ok {
			return fmt.Errorf("input '%s' expects number, got %T", path, value)
		}
		if propertySchema.Minimum != nil && numberValue < propertySchema.GetMinimum() {
			return fmt.Errorf("input '%s' must be >= %v", path, propertySchema.GetMinimum())
		}
		if propertySchema.Maximum != nil && numberValue > propertySchema.GetMaximum() {
			return fmt.Errorf("input '%s' must be <= %v", path, propertySchema.GetMaximum())
		}
		return nil
	case "integer":
		integerValue, ok := coerceInteger(value)
		if !ok {
			return fmt.Errorf("input '%s' expects integer, got %T", path, value)
		}
		if propertySchema.Minimum != nil && float64(integerValue) < propertySchema.GetMinimum() {
			return fmt.Errorf("input '%s' must be >= %v", path, propertySchema.GetMinimum())
		}
		if propertySchema.Maximum != nil && float64(integerValue) > propertySchema.GetMaximum() {
			return fmt.Errorf("input '%s' must be <= %v", path, propertySchema.GetMaximum())
		}
		return nil
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("input '%s' expects boolean, got %T", path, value)
		}
		return nil
	case "array":
		arrayValue, ok := toAnySlice(value)
		if !ok {
			return fmt.Errorf("input '%s' expects array, got %T", path, value)
		}
		for itemIndex, itemValue := range arrayValue {
			if err := validatePropertySchemaValue(fmt.Sprintf("%s[%d]", path, itemIndex), itemValue, propertySchema.GetItems()); err != nil {
				return err
			}
		}
		return nil
	case "object":
		objectValue, ok := toStringAnyMap(value)
		if !ok {
			return fmt.Errorf("input '%s' expects object, got %T", path, value)
		}
		for _, requiredPropertyName := range propertySchema.GetRequired() {
			if _, exists := objectValue[requiredPropertyName]; !exists {
				return fmt.Errorf("input '%s' missing required property %q", path, requiredPropertyName)
			}
		}
		for nestedPropertyName, nestedPropertySchema := range propertySchema.GetProperties() {
			nestedValue, exists := objectValue[nestedPropertyName]
			if !exists {
				continue
			}
			nestedPath := fmt.Sprintf("%s.%s", path, nestedPropertyName)
			if err := validatePropertySchemaValue(nestedPath, nestedValue, nestedPropertySchema); err != nil {
				return err
			}
		}
		return nil
	default:
		return nil
	}
}

func valueInPropertyEnum(value any, enumValues []*structpb.Value) bool {
	normalizedValue := normalizeComparableJSONValue(value)
	for _, enumValue := range enumValues {
		if enumValue == nil {
			continue
		}
		if reflect.DeepEqual(normalizedValue, normalizeComparableJSONValue(enumValue.AsInterface())) {
			return true
		}
	}
	return false
}

func normalizeComparableJSONValue(value any) any {
	switch typedValue := value.(type) {
	case int:
		return float64(typedValue)
	case int32:
		return float64(typedValue)
	case int64:
		return float64(typedValue)
	case float32:
		return float64(typedValue)
	case float64:
		return typedValue
	case []interface{}:
		normalizedSlice := make([]any, len(typedValue))
		for index, itemValue := range typedValue {
			normalizedSlice[index] = normalizeComparableJSONValue(itemValue)
		}
		return normalizedSlice
	case map[string]interface{}:
		normalizedMap := make(map[string]any, len(typedValue))
		for key, itemValue := range typedValue {
			normalizedMap[key] = normalizeComparableJSONValue(itemValue)
		}
		return normalizedMap
	default:
		return typedValue
	}
}

func toAnySlice(value any) ([]any, bool) {
	typedValue, ok := value.([]interface{})
	if !ok {
		return nil, false
	}
	return typedValue, true
}

func toStringAnyMap(value any) (map[string]any, bool) {
	typedValue, ok := value.(map[string]interface{})
	if !ok {
		return nil, false
	}
	stringMap := make(map[string]any, len(typedValue))
	for key, nestedValue := range typedValue {
		stringMap[key] = nestedValue
	}
	return stringMap, true
}

func coerceNumber(value any) (float64, bool) {
	switch typedValue := value.(type) {
	case int:
		return float64(typedValue), true
	case int32:
		return float64(typedValue), true
	case int64:
		return float64(typedValue), true
	case float32:
		return float64(typedValue), true
	case float64:
		return typedValue, true
	default:
		return 0, false
	}
}

func coerceInteger(value any) (int64, bool) {
	switch typedValue := value.(type) {
	case int:
		return int64(typedValue), true
	case int32:
		return int64(typedValue), true
	case int64:
		return typedValue, true
	case float64:
		if math.Trunc(typedValue) != typedValue {
			return 0, false
		}
		return int64(typedValue), true
	case float32:
		if math.Trunc(float64(typedValue)) != float64(typedValue) {
			return 0, false
		}
		return int64(typedValue), true
	default:
		return 0, false
	}
}

// validateProtoGroupInput validates nested inputs within a group.
func validateProtoGroupInput(groupName string, groupInput *reliantv1.Input, inputs map[string]any, path []string, vt ValueTypes, result *Result) {
	nested := model.GetGroupInputs(groupInput)
	if len(nested) == 0 {
		return
	}

	// Get the provided values for this group (if any)
	var groupValues map[string]any
	if providedGroup, exists := inputs[groupName]; exists {
		if mapVal, ok := providedGroup.(map[string]any); ok {
			groupValues = mapVal
		} else if mapVal, ok := providedGroup.(map[string]interface{}); ok {
			groupValues = make(map[string]any, len(mapVal))
			for k, v := range mapVal {
				groupValues[k] = v
			}
		}
	}

	// Validate each nested input
	for nestedName, nestedInput := range nested {
		if nestedInput == nil {
			continue
		}

		nestedPath := append(path, nestedName)
		var providedValue any
		provided := false

		if groupValues != nil {
			providedValue, provided = groupValues[nestedName]
		}

		// Check required inputs are provided
		if model.IsInputRequired(nestedInput) && (!provided || providedValue == nil) {
			result.AddError(CategoryInput, nestedPath, "",
				fmt.Sprintf("required input '%s.%s' is not provided", groupName, nestedName))
			continue
		}

		// If not provided, skip
		if !provided || providedValue == nil {
			continue
		}

		// Validate type/constraints of provided value
		if err := validateProtoInputType(nestedName, providedValue, nestedInput, vt); err != nil {
			result.AddError(CategoryInput, nestedPath, "",
				fmt.Sprintf("input '%s.%s': %s", groupName, nestedName, err.Error()))
		}
	}

	// Check for unknown inputs within the group
	if groupValues != nil {
		var unknownNested []string
		for key := range groupValues {
			if _, exists := nested[key]; !exists {
				unknownNested = append(unknownNested, groupName+"."+key)
			}
		}
		if len(unknownNested) > 0 {
			sort.Strings(unknownNested)
			result.AddError(CategoryInput, path, "",
				fmt.Sprintf("unknown input(s): %s", strings.Join(unknownNested, ", ")))
		}
	}
}

// ValidateParams checks values someone supplies for a workflow's inputs ahead
// of a run (an activation's params): each must name a declared input and fit
// its type and constraints. Unlike ValidateInputs it does not require the
// inputs nobody supplied, because the rest come from the event, defaults or
// presets when the run starts. Group inputs are left to the launch, which
// sees them nested. It returns one message per problem, in name order.
func ValidateParams(declared map[string]*reliantv1.Input, params map[string]any) []string {
	return ValidateParamsWith(declared, params, nil)
}

// ValidateParamsWith is ValidateParams with the resolver for integration input
// types; nil means the embedded catalog's.
func ValidateParamsWith(declared map[string]*reliantv1.Input, params map[string]any, types ValueTypes) []string {
	vt := typesOrDefault(types)
	names := make([]string, 0, len(params))
	for name := range params {
		names = append(names, name)
	}
	sort.Strings(names)

	var valid []string
	for name, input := range declared {
		if input != nil && !model.IsGroupInput(input) {
			valid = append(valid, name)
		}
	}
	sort.Strings(valid)

	var problems []string
	for _, name := range names {
		input := declared[name]
		switch {
		case input == nil:
			msg := fmt.Sprintf("%q is not an input of this workflow", name)
			if hint := suggestSimilar(name, valid); hint != "" {
				msg += "; " + hint
			}
			problems = append(problems, msg)
		case model.IsGroupInput(input) || params[name] == nil:
		default:
			if err := validateProtoInputType(name, params[name], input, vt); err != nil {
				problems = append(problems, err.Error())
			}
		}
	}
	return problems
}
