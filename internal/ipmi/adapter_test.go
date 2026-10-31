package ipmi

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

type protocolRunner struct {
	calls    [][]string
	board    string
	firmware string
	duties   string
	writeErr error
	after    string
}

func (r *protocolRunner) run(_ context.Context, args ...string) (string, error) {
	r.calls = append(r.calls, append([]string(nil), args...))
	switch strings.Join(args, " ") {
	case "mc info":
		return "Device ID : 32\nFirmware Revision : " + r.firmware, nil
	case "fru print":
		return "FRU Device Description : Builtin FRU Device (ID 0)\n Board Mfg : ASRock Rack\n Board Product : " + r.board + "\nFRU Device Description : Expansion (ID 1)\n Board Mfg : Other\n Board Product : Wrong", nil
	case "sdr type Fan":
		return "FAN1 | 60h | ok | 7.1 | 1500 RPM\nFAN2 | 62h | ns | 7.1 | No Reading\nFAN3 | 66h | ok | 7.1 | 1200 RPM", nil
	case "raw 0x3a 0x02":
		return r.duties, nil
	default:
		if len(args) != 11 || strings.Join(args[:3], " ") != "raw 0x3a 0x01" {
			return "", fmt.Errorf("unexpected command %v", args)
		}
		if r.after != "" {
			r.duties = r.after
		} else {
			parts := []string{}
			for _, arg := range args[3:] {
				parts = append(parts, strings.TrimPrefix(arg, "0x"))
			}
			r.duties = strings.Join(parts, " ")
		}
		return "", r.writeErr
	}
}

func protocolFixture() (*localAdapter, *protocolRunner, Identity) {
	runner := &protocolRunner{board: "E3C246D2I", firmware: "1.64", duties: "00 28 00 00 64 00 00 00"}
	return &localAdapter{runner: runner}, runner, Identity{Board: "E3C246D2I", Manufacturer: "ASRock Rack", Firmware: "1.64"}
}

func TestProfilePreservesOtherSlotsAndVerifiesReadback(t *testing.T) {
	a, runner, id := protocolFixture()
	reading, err := a.Set(context.Background(), id, 3, 35)
	if err != nil {
		t.Fatal(err)
	}
	want := []int{0, 40, 35, 0, 100, 0, 0, 0}
	if !reflect.DeepEqual(reading.Duties, want) {
		t.Fatalf("duties %v", reading.Duties)
	}
	wantArgs := []string{"raw", "0x3a", "0x01", "0x00", "0x28", "0x23", "0x00", "0x64", "0x00", "0x00", "0x00"}
	if !reflect.DeepEqual(runner.calls[3], wantArgs) {
		t.Fatalf("write %v", runner.calls[3])
	}
	if reading.Fans[2].RPM == nil || *reading.Fans[2].RPM != 1200 || reading.Fans[1].RPM != nil {
		t.Fatalf("sensors %+v", reading.Fans)
	}
	reading, err = a.Set(context.Background(), id, 3, 0)
	if err != nil || reading.Duties[2] != 0 || reading.Duties[1] != 40 {
		t.Fatalf("restore %v %v", reading, err)
	}
}

func TestProfileRefusesWrongIdentityAndInvalidInputBeforeWrite(t *testing.T) {
	for _, test := range []struct {
		name, board, firmware string
		slot, duty            int
	}{
		{"wrong-board", "E3C242D2I", "1.64", 3, 35},
		{"wrong-firmware", "E3C246D2I", "2.00", 3, 35},
		{"slot-zero", "E3C246D2I", "1.64", 0, 35},
		{"slot-nine", "E3C246D2I", "1.64", 9, 35},
		{"over-duty", "E3C246D2I", "1.64", 3, 101},
		{"negative-duty", "E3C246D2I", "1.64", 3, -1},
	} {
		t.Run(test.name, func(t *testing.T) {
			a, runner, id := protocolFixture()
			runner.board, runner.firmware = test.board, test.firmware
			if _, err := a.Set(context.Background(), id, test.slot, test.duty); err == nil {
				t.Fatal("accepted invalid write")
			}
			for _, call := range runner.calls {
				if call[0] == "raw" {
					t.Fatalf("OEM command before validation: %v", call)
				}
			}
		})
	}
}

func TestUnsupportedBoardOnlyMonitorsStandardSensors(t *testing.T) {
	a, runner, _ := protocolFixture()
	runner.board = "Other"
	r, err := a.Read(context.Background())
	if err != nil || len(r.Fans) != 3 || r.Duties != nil {
		t.Fatalf("reading %+v %v", r, err)
	}
	for _, call := range runner.calls {
		if call[0] == "raw" {
			t.Fatal("OEM command on unknown board")
		}
	}
}

func TestMalformedProfileNeverWrites(t *testing.T) {
	for _, text := range []string{"", "00 00 00", "00 00 00 00 00 00 00 00 00", "00 00 zz 00 00 00 00 00", "0 00 00 00 00 00 00 00"} {
		a, runner, id := protocolFixture()
		runner.duties = text
		if _, err := a.Set(context.Background(), id, 3, 35); err == nil {
			t.Fatalf("accepted %q", text)
		}
		if len(runner.calls) != 3 {
			t.Fatalf("unexpected commands %v", runner.calls)
		}
	}
}

func TestProfileRefusesOutOfRangeReadbackInEverySlot(t *testing.T) {
	// The 1.64 firmware rejects the complete eight-byte SET if any byte exceeds
	// 100, including an unselected slot. Never resend an unknown encoding.
	for slot := 0; slot < 8; slot++ {
		for _, invalid := range []string{"65", "ff"} {
			a, runner, id := protocolFixture()
			bytes := strings.Fields(runner.duties)
			bytes[slot] = invalid
			runner.duties = strings.Join(bytes, " ")
			if _, err := a.Set(context.Background(), id, 3, 35); err == nil || !strings.Contains(err.Error(), "0..100") {
				t.Fatalf("slot %d value %s was accepted: %v", slot+1, invalid, err)
			}
			if len(runner.calls) != 3 {
				t.Fatalf("unexpected command after invalid readback: %v", runner.calls)
			}
		}
	}
}

func TestProfileChecksUnselectedReadbackAndReportsUncertainWrite(t *testing.T) {
	t.Run("unselected byte changed", func(t *testing.T) {
		a, runner, id := protocolFixture()
		runner.after = "00 00 23 00 64 00 00 00"
		_, err := a.Set(context.Background(), id, 3, 35)
		if err == nil || !strings.Contains(err.Error(), "slot 2") {
			t.Fatalf("missing readback failure: %v", err)
		}
	})
	t.Run("command failed after possible application", func(t *testing.T) {
		a, runner, id := protocolFixture()
		expectedErr := errors.New("transport failed")
		runner.writeErr = expectedErr
		r, err := a.Set(context.Background(), id, 3, 35)
		if !errors.Is(err, expectedErr) || len(r.Duties) != 8 || r.Duties[2] != 35 {
			t.Fatalf("uncertainty lost: %+v %v", r, err)
		}
	})
}

func TestFanParserInvalidAndAlarmReadings(t *testing.T) {
	fans, err := parseFans("FAN1 | 0 RPM | cr\nFAN2 | NaN RPM | ok\nFAN3 | -1 RPM | ok\nFAN4 | 1e309 RPM | ok\nFAN5 | 1234 RPM | ok")
	if err != nil {
		t.Fatal(err)
	}
	if fans[0].Status != "alarm" || fans[0].RPM == nil || *fans[0].RPM != 0 {
		t.Fatal(fans[0])
	}
	for _, fan := range fans[1:4] {
		if fan.RPM != nil || fan.Status != "invalid" {
			t.Fatal(fan)
		}
	}
	if fans[4].Status != "ok" || *fans[4].RPM != 1234 {
		t.Fatal(fans[4])
	}
	for _, text := range []string{"", "bad row", "FAN3 | 10 RPM | ok\nFAN3 | 20 RPM | ok"} {
		if _, err := parseFans(text); err == nil {
			t.Fatalf("accepted %q", text)
		}
	}
}

func TestIdentityRejectsMissingOrAmbiguousBuiltinBoard(t *testing.T) {
	for _, fru := range []string{"Board Product : E3C246D2I", "Board Product : E3C246D2I\nBoard Product : E3C246D2I\nBoard Mfg : ASRock Rack", "FRU Device Description : Expansion (ID 1)\nBoard Product : E3C246D2I\nBoard Mfg : ASRock Rack"} {
		if _, err := parseIdentity("Firmware Revision : 1.23", fru); err == nil {
			t.Fatalf("accepted %q", fru)
		}
	}
}
