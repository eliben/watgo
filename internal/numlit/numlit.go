package numlit

import (
	"fmt"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// ParseIntBits parses s as a WAT integer literal and returns its two's
// complement bit representation for the requested width.
// bits must be 32 or 64.
func ParseIntBits(s string, bits int) (uint64, error) {
	if bits != 32 && bits != 64 {
		return 0, fmt.Errorf("unsupported integer width %d", bits)
	}

	clean := strings.ReplaceAll(s, "_", "")
	neg := false
	if len(clean) > 0 {
		switch clean[0] {
		case '+':
			clean = clean[1:]
		case '-':
			neg = true
			clean = clean[1:]
		}
	}
	if clean == "" {
		return 0, fmt.Errorf("invalid integer literal %q", s)
	}

	base := 10
	if strings.HasPrefix(clean, "0x") || strings.HasPrefix(clean, "0X") {
		base = 16
		clean = clean[2:]
		if clean == "" {
			return 0, fmt.Errorf("invalid integer literal %q", s)
		}
	}

	u, err := strconv.ParseUint(clean, base, bits)
	if err != nil {
		return 0, fmt.Errorf("invalid integer literal %q", s)
	}
	if neg {
		u = ^u + 1
	}
	if bits == 32 {
		u &= (1 << 32) - 1
	}
	return u, nil
}

// ParseF32Bits parses s as a WAT f32 literal and returns IEEE-754 bits.
func ParseF32Bits(s string) (uint32, error) {
	clean := strings.ReplaceAll(s, "_", "")
	if clean == "" {
		return 0, fmt.Errorf("invalid f32 literal %q", s)
	}

	sign, mag := splitSign(clean)
	switch mag {
	case "inf":
		if sign < 0 {
			return 0xff800000, nil
		}
		return 0x7f800000, nil
	case "nan":
		if sign < 0 {
			return 0xffc00000, nil
		}
		return 0x7fc00000, nil
	}
	if strings.HasPrefix(mag, "nan:0x") {
		payload, err := strconv.ParseUint(mag[6:], 16, 32)
		if err != nil || payload == 0 || payload > 0x7fffff {
			return 0, fmt.Errorf("invalid f32 literal %q", s)
		}
		bits := uint32(0x7f800000 | payload)
		if sign < 0 {
			bits |= 0x80000000
		}
		return bits, nil
	}

	if bits, ok := parseF32WithParseFloat(clean); ok {
		return bits, nil
	}

	// WAT allows hex float literals without explicit p-exponent.
	if (strings.HasPrefix(mag, "0x") || strings.HasPrefix(mag, "0X")) &&
		strings.Contains(mag, ".") &&
		!strings.ContainsAny(mag, "pP") {
		if bits, ok := parseF32WithParseFloat(clean + "p0"); ok {
			return bits, nil
		}
	}

	if bits, ok := parseIntegerLiteralF32Bits(clean); ok {
		return bits, nil
	}

	return 0, fmt.Errorf("invalid f32 literal %q", s)
}

// ParseF64Bits parses s as a WAT f64 literal and returns IEEE-754 bits.
func ParseF64Bits(s string) (uint64, error) {
	clean := strings.ReplaceAll(s, "_", "")
	if clean == "" {
		return 0, fmt.Errorf("invalid f64 literal %q", s)
	}

	sign, mag := splitSign(clean)
	switch mag {
	case "inf":
		if sign < 0 {
			return 0xfff0000000000000, nil
		}
		return 0x7ff0000000000000, nil
	case "nan":
		if sign < 0 {
			return 0xfff8000000000000, nil
		}
		return 0x7ff8000000000000, nil
	}
	if strings.HasPrefix(mag, "nan:0x") {
		payload, err := strconv.ParseUint(mag[6:], 16, 64)
		if err != nil || payload == 0 || payload > 0x000fffffffffffff {
			return 0, fmt.Errorf("invalid f64 literal %q", s)
		}
		bits := uint64(0x7ff0000000000000 | payload)
		if sign < 0 {
			bits |= 0x8000000000000000
		}
		return bits, nil
	}

	if bits, ok := parseF64WithParseFloat(clean); ok {
		return bits, nil
	}

	// WAT allows hex float literals without explicit p-exponent.
	if (strings.HasPrefix(mag, "0x") || strings.HasPrefix(mag, "0X")) &&
		strings.Contains(mag, ".") &&
		!strings.ContainsAny(mag, "pP") {
		if bits, ok := parseF64WithParseFloat(clean + "p0"); ok {
			return bits, nil
		}
	}

	if bits, ok := parseIntegerLiteralF64Bits(clean); ok {
		return bits, nil
	}

	return 0, fmt.Errorf("invalid f64 literal %q", s)
}

func parseF32WithParseFloat(s string) (uint32, bool) {
	f, err := strconv.ParseFloat(s, 32)
	if err != nil {
		return 0, false
	}
	return math.Float32bits(float32(f)), true
}

func parseF64WithParseFloat(s string) (uint64, bool) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, false
	}
	return math.Float64bits(f), true
}

func parseIntegerLiteralF32Bits(s string) (uint32, bool) {
	sign, mag := splitSign(s)
	if mag == "" || strings.Contains(mag, ".") {
		return 0, false
	}
	if strings.HasPrefix(mag, "0x") || strings.HasPrefix(mag, "0X") {
		if strings.ContainsAny(mag, "pP") {
			return 0, false
		}
	} else if strings.ContainsAny(mag, "eE") {
		return 0, false
	}

	bi, ok := new(big.Int).SetString(mag, 0)
	if !ok {
		return 0, false
	}

	bf := new(big.Float).SetMode(big.ToNearestEven)
	bf.SetInt(bi)
	if sign < 0 {
		bf.Neg(bf)
	}

	f, _ := bf.Float32()
	if math.IsInf(float64(f), 0) {
		return 0, false
	}
	return math.Float32bits(f), true
}

func parseIntegerLiteralF64Bits(s string) (uint64, bool) {
	sign, mag := splitSign(s)
	if mag == "" || strings.Contains(mag, ".") {
		return 0, false
	}
	if strings.HasPrefix(mag, "0x") || strings.HasPrefix(mag, "0X") {
		if strings.ContainsAny(mag, "pP") {
			return 0, false
		}
	} else if strings.ContainsAny(mag, "eE") {
		return 0, false
	}

	bi, ok := new(big.Int).SetString(mag, 0)
	if !ok {
		return 0, false
	}

	bf := new(big.Float).SetMode(big.ToNearestEven)
	bf.SetInt(bi)
	if sign < 0 {
		bf.Neg(bf)
	}

	f, _ := bf.Float64()
	if math.IsInf(f, 0) {
		return 0, false
	}
	return math.Float64bits(f), true
}

func splitSign(s string) (float64, string) {
	if s == "" {
		return 1, s
	}
	switch s[0] {
	case '+':
		return 1, s[1:]
	case '-':
		return -1, s[1:]
	default:
		return 1, s
	}
}
