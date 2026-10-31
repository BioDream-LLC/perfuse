package engine

import (
	"reflect"
	"testing"
)

// TestTheStatsSnapshotCopiesEveryCounter guards Channel.Stats against forgetting a field.
//
// Queued and DestQueued were counted but left out of the copy, so the dashboard showed a backlog of zero while a
// destination's queue was filling. This fills every counter and checks each one comes out of the snapshot, so a field
// added later cannot be forgotten the same way.
func TestTheStatsSnapshotCopiesEveryCounter(t *testing.T) {
	c := &Channel{stats: newStats()}
	v := reflect.ValueOf(&c.stats).Elem()
	for i := range v.NumField() {
		f := v.Field(i)
		switch f.Kind() {
		case reflect.Int64:
			f.SetInt(int64(i + 1))
		case reflect.Map:
			f.SetMapIndex(reflect.ValueOf("dest"), reflect.ValueOf(int64(i+1)))
		default:
			t.Fatalf("Stats.%s is a %s; teach this test about it", v.Type().Field(i).Name, f.Kind())
		}
	}
	got := reflect.ValueOf(c.Stats())
	for i := range v.NumField() {
		if !reflect.DeepEqual(got.Field(i).Interface(), v.Field(i).Interface()) {
			t.Errorf("Stats.%s: snapshot has %v, channel has %v", v.Type().Field(i).Name, got.Field(i), v.Field(i))
		}
	}
}
