package numlit

import (
	"testing"
)

func TestParseIntBits(t *testing.T) {
	tests := []struct {
		lit  string
		bits int
		want uint64
	}{
		{lit: "123", bits: 32, want: 123},
		{lit: "-1", bits: 32, want: 0xffffffff},
		{lit: "0x1_2_3", bits: 32, want: 0x123},
		{lit: "-0x8000000000000000", bits: 64, want: 0x8000000000000000},
	}

	for _, tt := range tests {
		got, err := ParseIntBits(tt.lit, tt.bits)
		if err != nil {
			t.Fatalf("ParseIntBits(%q, %d) failed: %v", tt.lit, tt.bits, err)
		}
		if got != tt.want {
			t.Fatalf("ParseIntBits(%q, %d)=%#x, want %#x", tt.lit, tt.bits, got, tt.want)
		}
	}
}

func TestParseIntBitsErrors(t *testing.T) {
	if _, err := ParseIntBits("10", 16); err == nil {
		t.Fatal("ParseIntBits with unsupported width succeeded, want error")
	}
	if _, err := ParseIntBits("0x", 32); err == nil {
		t.Fatal("ParseIntBits with invalid literal succeeded, want error")
	}
}

func TestParseF32Bits(t *testing.T) {
	tests := []struct {
		lit  string
		want uint32
	}{
		{lit: "1.5", want: 0x3fc00000},
		{lit: "0x1.8p+1", want: 0x40400000},
		{lit: "0x1.8", want: 0x3fc00000}, // Hex float without explicit p-exponent.
		{lit: "0xf32", want: 0x45732000}, // Integer form routed through big-int conversion.
		{lit: "nan:0x200001", want: 0x7fa00001},
		{lit: "-inf", want: 0xff800000},
	}

	for _, tt := range tests {
		got, err := ParseF32Bits(tt.lit)
		if err != nil {
			t.Fatalf("ParseF32Bits(%q) failed: %v", tt.lit, err)
		}
		if got != tt.want {
			t.Fatalf("ParseF32Bits(%q)=%#x, want %#x", tt.lit, got, tt.want)
		}
	}
}

func TestParseF32BitsErrors(t *testing.T) {
	if _, err := ParseF32Bits("nan:0x0"); err == nil {
		t.Fatal("ParseF32Bits accepted zero NaN payload, want error")
	}
	if _, err := ParseF32Bits(""); err == nil {
		t.Fatal("ParseF32Bits accepted empty literal, want error")
	}
}

func TestParseF64Bits(t *testing.T) {
	tests := []struct {
		lit  string
		want uint64
	}{
		{lit: "1.64", want: 0x3ffa3d70a3d70a3d},
		{lit: "0x1.8p+1", want: 0x4008000000000000},
		{lit: "0x1.8", want: 0x3ff8000000000000}, // Hex float without explicit p-exponent.
		{lit: "0xf64", want: 0x40aec80000000000},
		{lit: "nan:0x8000000000001", want: 0x7ff8000000000001},
		{lit: "-inf", want: 0xfff0000000000000},
	}

	for _, tt := range tests {
		got, err := ParseF64Bits(tt.lit)
		if err != nil {
			t.Fatalf("ParseF64Bits(%q) failed: %v", tt.lit, err)
		}
		if got != tt.want {
			t.Fatalf("ParseF64Bits(%q)=%#x, want %#x", tt.lit, got, tt.want)
		}
	}
}

func TestParseF64BitsErrors(t *testing.T) {
	if _, err := ParseF64Bits("nan:0x0"); err == nil {
		t.Fatal("ParseF64Bits accepted zero NaN payload, want error")
	}
	if _, err := ParseF64Bits("0x0p"); err == nil {
		t.Fatal("ParseF64Bits accepted malformed literal, want error")
	}
}
