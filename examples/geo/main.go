// geo — points, radius, bbox, nearest-k with real coordinates.
//
// Four German cities stored with their real lat/lon (the [lat, lon]
// array encoding; a {"lat": …, "lon": …} map encodes the same point).
// Distances are haversine kilometres:
//
//	radius 600 km from central Berlin (52.52, 13.40):
//	  berlin 0.000000, potsdam 26.621424, hamburg 255.120591,
//	  munchen 503.833264 — nearest first, inclusive boundary.
//	bbox (47..55, 5..15): all four, key order, the 0.0 sentinel
//	  (a box has no center to measure from).
//	nearest 2: berlin, potsdam — exact haversine order.
//
// These are the same points and tolerances the engine's golden geo
// fixture asserts (~1e-6 km).
//
// Run: go run ./examples/geo   (after `make deps`)
package main

import (
	"fmt"
	"strings"

	"github.com/corvid-db/corvid-go"
)

func must(err error) {
	if err != nil {
		panic(err)
	}
}

var cities = []struct {
	name string
	lat  float64
	lon  float64
}{
	{"berlin", 52.52, 13.40},
	{"potsdam", 52.40, 13.06},
	{"hamburg", 53.55, 9.99},
	{"munchen", 48.14, 11.58},
}

func show(label string, hits []corvid.GeoHit, err error) {
	if err != nil {
		panic(err)
	}
	parts := make([]string, len(hits))
	for i, h := range hits {
		parts[i] = fmt.Sprintf("%s %.6fkm", h.Key, h.DistanceKm)
	}
	fmt.Printf("%-34s [%s]\n", label, strings.Join(parts, " "))
}

func main() {
	db, err := corvid.OpenMemory()
	if err != nil {
		panic(err)
	}
	defer func() { must(db.Close()) }()

	places, err := db.Collection("places")
	if err != nil {
		panic(err)
	}
	defer places.Close()

	for _, c := range cities {
		must(places.Insert([]byte(c.name), map[string]any{
			"name": c.name,
			"loc":  []any{c.lat, c.lon}, // the [lat, lon] array encoding
		}))
	}
	must(places.CreateGeoIndex("loc"))

	hits, err := places.GeoWithinRadius("loc", 52.52, 13.40, 600.0)
	show("within 600km of Berlin:", hits, err)
	hits, err = places.GeoWithinBBox("loc", 47, 5, 55, 15)
	show("bbox 47..55N, 5..15E:", hits, err)
	hits, err = places.GeoNearest("loc", 52.52, 13.40, 2)
	show("nearest 2 to Berlin:", hits, err)
}
