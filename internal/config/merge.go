package config

import (
	"math"
	"reflect"
)

// MergeChanged copies onto fresh every leaf of current that differs from
// launch, and nothing else. It is the TUI's exit save: fresh is config.toml
// read again at exit, launch the copy handed to the Model at start, current
// the Model's copy now. A setting another writer (the browser, another TUI, a
// remote install) stored meanwhile survives unless THIS TUI changed the same
// leaf.
//
// A struct is walked into. Every other field is one leaf: scalars, and every
// slice and map AS A WHOLE (Destinations, Remote.Hosts) — merging inside a
// list would invent orders nobody wrote. Leaves compare with reflect.DeepEqual
// (two NaN floats count as equal). A copied slice or map is copied, so fresh
// never shares memory with the Model. Elements are copied shallowly: today
// every element type is a struct of strings.
//
// A leaf set and then restored by the TUI compares equal to launch, so the
// fresh value wins: the TUI did not change it in total.
//
// Sandbox.Auth and Sandbox.SharedClaudeConfig are ONE choice: the sign-in
// row writes both, and token + shared is the pair it exists to never write.
// A change to either carries both, or the browser's "token" under this TUI's
// "shared" (which leaves Auth at launch) lands as exactly that pair.
func MergeChanged(fresh *Config, launch, current Config) {
	mergeValue(reflect.ValueOf(fresh).Elem(), reflect.ValueOf(launch), reflect.ValueOf(current))
	if launch.Sandbox.Auth != current.Sandbox.Auth || launch.Sandbox.SharedClaudeConfig != current.Sandbox.SharedClaudeConfig {
		fresh.Sandbox.Auth = current.Sandbox.Auth
		fresh.Sandbox.SharedClaudeConfig = current.Sandbox.SharedClaudeConfig
	}
}

func mergeValue(dst, launch, cur reflect.Value) {
	if dst.Kind() == reflect.Struct {
		for i := 0; i < dst.NumField(); i++ {
			if !dst.Type().Field(i).IsExported() {
				continue
			}
			mergeValue(dst.Field(i), launch.Field(i), cur.Field(i))
		}
		return
	}
	if leafEqual(launch, cur) {
		return
	}
	dst.Set(copyRef(cur))
}

func leafEqual(a, b reflect.Value) bool {
	if a.Kind() == reflect.Float32 || a.Kind() == reflect.Float64 {
		x, y := a.Float(), b.Float()
		return x == y || (math.IsNaN(x) && math.IsNaN(y))
	}
	return reflect.DeepEqual(a.Interface(), b.Interface())
}

// copyRef returns v with a new backing slice or map; any other value as is.
func copyRef(v reflect.Value) reflect.Value {
	switch v.Kind() {
	case reflect.Slice:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeSlice(v.Type(), v.Len(), v.Len())
		reflect.Copy(out, v)
		return out
	case reflect.Map:
		if v.IsNil() {
			return reflect.Zero(v.Type())
		}
		out := reflect.MakeMapWithSize(v.Type(), v.Len())
		it := v.MapRange()
		for it.Next() {
			out.SetMapIndex(it.Key(), it.Value())
		}
		return out
	}
	return v
}

// Clone returns c with its own slices and maps, so the launch copy the exit
// save compares against cannot change under it.
func Clone(c Config) Config {
	out := c
	cloneRefs(reflect.ValueOf(&out).Elem())
	return out
}

func cloneRefs(v reflect.Value) {
	switch v.Kind() {
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				cloneRefs(v.Field(i))
			}
		}
	case reflect.Slice, reflect.Map:
		v.Set(copyRef(v))
	}
}
