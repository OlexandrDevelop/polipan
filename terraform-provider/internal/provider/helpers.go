// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/polipan/terraform-provider-polipan/internal/client"
)

// put copies a Terraform value into a request body when it is known and not null.
// An unset attribute is not sent, so the server default stays.
func put(body client.Obj, key string, v attr.Value) {
	if v == nil || v.IsNull() || v.IsUnknown() {
		return
	}
	switch t := v.(type) {
	case types.String:
		body[key] = t.ValueString()
	case types.Bool:
		body[key] = t.ValueBool()
	case types.Int64:
		body[key] = t.ValueInt64()
	case types.Float64:
		body[key] = t.ValueFloat64()
	case types.List:
		body[key] = stringsOf(t.Elements())
	case types.Set:
		body[key] = stringsOf(t.Elements())
	case types.Map:
		m := map[string]string{}
		for k, e := range t.Elements() {
			if s, ok := e.(types.String); ok {
				m[k] = s.ValueString()
			}
		}
		body[key] = m
	}
}

// putEmpty is like put, but a null value is sent as the empty value: used by updates, where
// removing an attribute from the config must remove it on the server.
func putEmpty(body client.Obj, key string, v attr.Value, empty any) {
	if v != nil && v.IsNull() {
		body[key] = empty
		return
	}
	put(body, key, v)
}

func stringsOf(elems []attr.Value) []string {
	out := make([]string, 0, len(elems))
	for _, e := range elems {
		if s, ok := e.(types.String); ok {
			out = append(out, s.ValueString())
		}
	}
	return out
}

// The set* helpers always copy the API value (a missing value gives null). They are for required
// and computed attributes. The opt* helpers copy only when the attribute is set in the state and the
// API has a value: a value the user did not write never makes a plan change.

func setStr(dst *types.String, o client.Obj, key string) {
	if s, ok := o[key].(string); ok {
		*dst = types.StringValue(s)
		return
	}
	*dst = types.StringNull()
}

func optStr(dst *types.String, o client.Obj, key string) {
	if dst.IsNull() {
		return
	}
	if s, ok := o[key].(string); ok {
		*dst = types.StringValue(s)
	}
}

func setBool(dst *types.Bool, o client.Obj, key string) {
	if b, ok := o[key].(bool); ok {
		*dst = types.BoolValue(b)
		return
	}
	*dst = types.BoolNull()
}

func optBool(dst *types.Bool, o client.Obj, key string) {
	if dst.IsNull() {
		return
	}
	if b, ok := o[key].(bool); ok {
		*dst = types.BoolValue(b)
	}
}

func setInt(dst *types.Int64, o client.Obj, key string) {
	if f, ok := o[key].(float64); ok {
		*dst = types.Int64Value(int64(f))
		return
	}
	*dst = types.Int64Null()
}

func optInt(dst *types.Int64, o client.Obj, key string) {
	if dst.IsNull() {
		return
	}
	if f, ok := o[key].(float64); ok {
		*dst = types.Int64Value(int64(f))
	}
}

func listValue(vals []string) types.List {
	elems := make([]attr.Value, len(vals))
	for i, v := range vals {
		elems[i] = types.StringValue(v)
	}
	return types.ListValueMust(types.StringType, elems)
}

func setValue(vals []string) types.Set {
	elems := make([]attr.Value, len(vals))
	for i, v := range vals {
		elems[i] = types.StringValue(v)
	}
	return types.SetValueMust(types.StringType, elems)
}

func optList(dst *types.List, o client.Obj, key string) {
	if dst.IsNull() {
		return
	}
	if _, ok := o[key].([]any); ok {
		*dst = listValue(client.Strings(o, key))
	}
}

func setList(dst *types.List, o client.Obj, key string) {
	if _, ok := o[key].([]any); ok {
		*dst = listValue(client.Strings(o, key))
		return
	}
	*dst = types.ListNull(types.StringType)
}

func optSet(dst *types.Set, o client.Obj, key string) {
	if dst.IsNull() {
		return
	}
	if _, ok := o[key].([]any); ok {
		*dst = setValue(client.Strings(o, key))
	}
}

func setSet(dst *types.Set, o client.Obj, key string) {
	if _, ok := o[key].([]any); ok {
		*dst = setValue(client.Strings(o, key))
		return
	}
	*dst = types.SetNull(types.StringType)
}

// scalarString turns a JSON scalar into text. Whole numbers have no decimal part.
func scalarString(v any) (string, bool) {
	switch t := v.(type) {
	case string:
		return t, true
	case bool:
		return strconv.FormatBool(t), true
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64), true
	}
	return "", false
}

// optStrMap reads a map of strings. It keeps only the keys that are in the state, so keys the
// server adds do not make a plan change. A key the server dropped is removed from the state.
func optStrMap(dst *types.Map, src client.Obj) {
	if dst.IsNull() || dst.IsUnknown() {
		return
	}
	out := map[string]attr.Value{}
	for k := range dst.Elements() {
		if v, ok := scalarString(src[k]); ok {
			out[k] = types.StringValue(v)
		}
	}
	*dst = types.MapValueMust(types.StringType, out)
}

// mapOf makes a plain map for a request.
func mapOf(m types.Map) map[string]any {
	out := map[string]any{}
	for k, e := range m.Elements() {
		if s, ok := e.(types.String); ok {
			out[k] = s.ValueString()
		}
	}
	return out
}

// parseJSONObj reads a JSON object from a string attribute. An empty or null value is an empty object.
func parseJSONObj(s types.String) (map[string]any, error) {
	out := map[string]any{}
	if s.IsNull() || s.IsUnknown() || s.ValueString() == "" {
		return out, nil
	}
	if err := json.Unmarshal([]byte(s.ValueString()), &out); err != nil {
		return nil, fmt.Errorf("the value must be a JSON object (use jsonencode): %w", err)
	}
	return out, nil
}

// putJSON copies a JSON object attribute into a request body.
func putJSON(body client.Obj, key string, s types.String) error {
	if s.IsNull() || s.IsUnknown() {
		return nil
	}
	obj, err := parseJSONObj(s)
	if err != nil {
		return fmt.Errorf("%s: %w", key, err)
	}
	body[key] = obj
	return nil
}

// syncJSON reads a JSON object attribute back. Only the keys the user wrote are compared. When
// the API has the same values, the text in the state is kept as it is, so spacing and key order
// never make a plan change.
func syncJSON(dst *types.String, api map[string]any) {
	if dst.IsNull() || dst.IsUnknown() {
		return
	}
	mine, err := parseJSONObj(*dst)
	if err != nil {
		return
	}
	subset := map[string]any{}
	for k := range mine {
		if v, ok := api[k]; ok {
			subset[k] = v
		}
	}
	if reflect.DeepEqual(mine, subset) {
		return
	}
	raw, err := json.Marshal(subset)
	if err == nil {
		*dst = types.StringValue(string(raw))
	}
}

// jsonText makes compact JSON text of an object.
func jsonText(o map[string]any) string {
	raw, err := json.Marshal(o)
	if err != nil {
		return "{}"
	}
	return string(raw)
}

// setStrMap copies every scalar value of an API object into a map of strings.
func setStrMap(dst *types.Map, src client.Obj) {
	out := map[string]attr.Value{}
	for k, v := range src {
		if s, ok := scalarString(v); ok {
			out[k] = types.StringValue(s)
		}
	}
	*dst = types.MapValueMust(types.StringType, out)
}
