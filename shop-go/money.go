package main

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// All money is stored as integer paise (₹1 = 100) so totals never drift.

func money(p int64) string {
	neg := ""
	if p < 0 {
		neg, p = "-", -p
	}
	if p%100 == 0 {
		return fmt.Sprintf("%s₹%d", neg, p/100)
	}
	return fmt.Sprintf("%s₹%d.%02d", neg, p/100, p%100)
}

func parseMoney(s string) (int64, error) {
	f, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || f < 0 || f > 10000000 {
		return 0, fmt.Errorf("invalid amount")
	}
	return int64(math.Round(f * 100)), nil
}

// plain number for forms: 6000 -> "60", 6050 -> "60.50"
func plain(p int64) string {
	if p%100 == 0 {
		return strconv.FormatInt(p/100, 10)
	}
	return fmt.Sprintf("%d.%02d", p/100, p%100)
}

// Pack sizes everything is sold in.
var packs = []int{1000, 500, 250}

func packLabel(g int) string {
	switch g {
	case 1000:
		return "1 kg"
	case 500:
		return "½ kg"
	case 250:
		return "¼ kg"
	}
	return fmt.Sprintf("%d g", g)
}

func packPrice(perKg int64, grams int) int64 {
	return (perKg*int64(grams) + 500) / 1000
}
