package memory

import (
	"reflect"
)

// The doubles in this package round-trip aggregates through clone, the
// in-memory stand-in for the Postgres adapter's row rehydration: Save
// stores an independent deep copy with the pending domain events consumed,
// and every read returns another copy. A live-pointer map would instead
// replay already-published events on the next save and let caller
// mutations corrupt the stored state — the exact bug this file exists to
// prevent (proven by TestClone_RoundTrip).
//
// The domain aggregates now expose Snapshot/Restore (the Postgres adapter's
// rehydration path), but those are per-aggregate typed APIs; clone stays
// generic over any aggregate the doubles store, so it keeps copying the
// structs through reflect. Writing an unexported field needs the
// reflect.NewAt + unsafe.Pointer recovery below; it stays inside this
// test-double package. No production adapter does this: Postgres rebuilds
// aggregates from rows via Snapshot/Restore, which is the same isolation by
// other means.

// clone returns a deep copy of *src with the aggregate's `events` slice
// dropped (the facts were published; a read-back aggregate has none
// pending, exactly like a rehydrated one). nil stays nil.
func clone[T any](src *T) *T {
	if src == nil {
		return nil
	}
	dst := new(T)
	d := reflect.ValueOf(dst).Elem()
	copyValue(d, reflect.ValueOf(src).Elem())
	if events := d.FieldByName("events"); events.IsValid() && events.CanAddr() {
		// Only the aggregate roots carry an events field; nested values
		// (receipt lines, policy rules) never do.
		writable(events).Set(reflect.Zero(events.Type()))
	}
	return dst
}

// copyValue deep-copies src into dst: structs field by field, slices into
// fresh backing arrays, everything else by value.
func copyValue(dst, src reflect.Value) {
	switch src.Kind() {
	case reflect.Struct:
		for i := 0; i < src.NumField(); i++ {
			copyValue(writable(dst.Field(i)), writable(src.Field(i)))
		}
	case reflect.Slice:
		if src.IsNil() {
			return // dst is already the zero value
		}
		s := reflect.MakeSlice(src.Type(), src.Len(), src.Len())
		for i := 0; i < src.Len(); i++ {
			copyValue(writable(s.Index(i)), writable(src.Index(i)))
		}
		writable(dst).Set(s)
	default:
		writable(dst).Set(writable(src))
	}
}

// writable recovers a settable view of a possibly unexported, but
// addressable, struct field.
func writable(v reflect.Value) reflect.Value {
	if v.CanSet() {
		return v
	}
	return reflect.NewAt(v.Type(), v.Addr().UnsafePointer()).Elem()
}
