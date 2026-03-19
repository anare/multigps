package main

import (
	"fmt"
	"math"
	"time"
)

// decimalToNMEA converts a decimal-degree value to the NMEA degree-minute
// string (DDMM.mmmmm or DDDMM.mmmmm) and its direction letter (N/S or E/W).
func decimalToNMEA(deg float64, isLon bool) (string, string) {
	abs := math.Abs(deg)
	d := math.Floor(abs)
	m := (abs - d) * 60.0

	var dir string
	if isLon {
		if deg >= 0 {
			dir = "E"
		} else {
			dir = "W"
		}
		return fmt.Sprintf("%03.0f%08.5f", d, m), dir
	}
	if deg >= 0 {
		dir = "N"
	} else {
		dir = "S"
	}
	return fmt.Sprintf("%02.0f%08.5f", d, m), dir
}

// nmeaChecksum computes the XOR checksum over all bytes between '$' and '*'.
func nmeaChecksum(body string) byte {
	var cs byte
	for i := 0; i < len(body); i++ {
		cs ^= body[i]
	}
	return cs
}

// buildSentence wraps body in the standard NMEA framing: $<body>*<HH>\r\n
func buildSentence(body string) string {
	return fmt.Sprintf("$%s*%02X\r\n", body, nmeaChecksum(body))
}

// GenerateGPGGA returns a GPGGA sentence (essential fix data).
//
//	$GPGGA,HHMMSS.ss,DDMM.mmmmm,N,DDDMM.mmmmm,E,Q,NN,H.H,A.A,M,G.G,M,,*hh
func GenerateGPGGA(lat, lon, alt float64, t time.Time) string {
	latStr, latDir := decimalToNMEA(lat, false)
	lonStr, lonDir := decimalToNMEA(lon, true)
	body := fmt.Sprintf(
		"GPGGA,%s,%s,%s,%s,%s,1,08,1.0,%.1f,M,0.0,M,,",
		t.UTC().Format("150405.00"),
		latStr, latDir,
		lonStr, lonDir,
		alt,
	)
	return buildSentence(body)
}

// GenerateGPRMC returns a GPRMC sentence (recommended minimum GPS data).
//
//	$GPRMC,HHMMSS.ss,A,DDMM.mmmmm,N,DDDMM.mmmmm,E,S.SS,C.CC,DDMMYY,,,A*hh
func GenerateGPRMC(lat, lon, speed, course float64, t time.Time) string {
	latStr, latDir := decimalToNMEA(lat, false)
	lonStr, lonDir := decimalToNMEA(lon, true)
	body := fmt.Sprintf(
		"GPRMC,%s,A,%s,%s,%s,%s,%.2f,%.2f,%s,,,A",
		t.UTC().Format("150405.00"),
		latStr, latDir,
		lonStr, lonDir,
		speed, course,
		t.UTC().Format("020106"),
	)
	return buildSentence(body)
}
