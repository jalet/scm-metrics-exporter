package main

import (
	"reflect"
	"slices"
	"testing"
)

func TestParseWatchNamespaces(t *testing.T) {
	tests := []struct {
		name    string
		give    string
		want    []string
		wantErr bool
	}{
		{"empty", "", nil, false},
		{"only separators", " , ,", nil, false},
		{"one", "scm-system", []string{"scm-system"}, false},
		{"trims and keeps order", " b , a", []string{"b", "a"}, false},
		{"drops duplicates", "a,b,a", []string{"a", "b"}, false},
		{"rejects invalid name", "a,Not_Valid", nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseWatchNamespaces(tt.give)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCacheOptions(t *testing.T) {
	if got := cacheOptions(nil); got.DefaultNamespaces != nil {
		t.Errorf("no namespaces: DefaultNamespaces = %v, want nil (cluster-wide)", got.DefaultNamespaces)
	}
	got := cacheOptions([]string{"a", "b"})
	keys := make([]string, 0, len(got.DefaultNamespaces))
	for ns := range got.DefaultNamespaces {
		keys = append(keys, ns)
	}
	slices.Sort(keys)
	if want := []string{"a", "b"}; !slices.Equal(keys, want) {
		t.Errorf("DefaultNamespaces keys = %v, want %v", keys, want)
	}
}
