package weather_test

import (
	"context"
	"testing"
	"time"

	"github.com/openmanet/openmanetd/internal/weather"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func noWait(context.Context, time.Duration) error { return nil }

// sen0657Regs is a plausible register table: 12.5 m/s from the east,
// 65.8 %RH, -10.1 degC (two's complement, the datasheet example), raw
// pressure 851, 100000 lux split across two registers, 2.5 mm of rain.
func sen0657Regs() map[uint16]uint16 {
	return map[uint16]uint16{
		0x01F4: 0x007D,
		0x01F6: 2,
		0x01F7: 90,
		0x01F8: 0x0292,
		0x01F9: 0xFF9B,
		0x01FD: 851,
		0x01FE: 0x0001,
		0x01FF: 0x86A0,
		0x0201: 25,
	}
}

func newSEN0657(dev *fakeModbusDevice) *weather.SEN0657 {
	return weather.NewSEN0657(dev, weather.SEN0657Options{Wait: noWait, Now: newFakeClock().Now})
}

func TestSEN0657_Read_decodesBlock(t *testing.T) {
	dev := newFakeModbusDevice(sen0657Regs())

	s, err := newSEN0657(dev).Read(context.Background())
	require.NoError(t, err)

	assert.InDelta(t, 12.5, s.WindSpeedMS, 1e-9)
	assert.InDelta(t, 90, s.WindDirectionDeg, 1e-9)
	assert.InDelta(t, 65.8, s.HumidityPct, 1e-9)
	assert.InDelta(t, -10.1, s.TemperatureC, 1e-9, "0xFF9B is -10.1 degC per the datasheet")
	assert.InDelta(t, 851, s.PressureHPa, 1e-9)
	assert.InDelta(t, 100000, s.LightLux, 1e-9, "high and low registers form one 32-bit lux value")
	assert.InDelta(t, 2.5, s.RainTotalMM, 1e-9)
	assert.Equal(t, weather.FieldTemperature|weather.FieldHumidity|weather.FieldPressure|weather.FieldWindSpeed|
		weather.FieldWindDirection|weather.FieldRainTotal|weather.FieldLight, s.Has)

	writes := dev.getWrites()
	require.Len(t, writes, 1, "one request covers every register")
	assert.Equal(t, withCRC([]byte{0x01, 0x03, 0x01, 0xF4, 0x00, 0x0E}), writes[0])
}

func TestSEN0657_Read_datasheetFrames(t *testing.T) {
	// The wiki's wind speed example, byte for byte, pins the CRC in both
	// directions: request 01 03 01 F4 00 01 C4 04, reply 01 03 02 00 7D 78 65.
	dev := newFakeModbusDevice(sen0657Regs())
	except := uint16(0x01F5)
	dev.exceptOn = &except
	dev.replies[string([]byte{0x01, 0x03, 0x01, 0xF4, 0x00, 0x01, 0xC4, 0x04})] = []byte{0x01, 0x03, 0x02, 0x00, 0x7D, 0x78, 0x65}

	s, err := newSEN0657(dev).Read(context.Background())
	require.NoError(t, err)
	assert.InDelta(t, 12.5, s.WindSpeedMS, 1e-9)
}

func TestSEN0657_Read_splitsAfterException(t *testing.T) {
	dev := newFakeModbusDevice(sen0657Regs())
	except := uint16(0x01F5)
	dev.exceptOn = &except
	sensor := newSEN0657(dev)

	s, err := sensor.Read(context.Background())
	require.NoError(t, err)
	assert.InDelta(t, 12.5, s.WindSpeedMS, 1e-9)
	assert.InDelta(t, -10.1, s.TemperatureC, 1e-9)
	assert.InDelta(t, 2.5, s.RainTotalMM, 1e-9)

	_, err = sensor.Read(context.Background())
	require.NoError(t, err)

	writes := dev.getWrites()
	require.Len(t, writes, 5, "block read, then two parts; later polls skip the block read")
	assert.Equal(t, []byte{0x01, 0x03, 0x01, 0xF4, 0x00, 0x01, 0xC4, 0x04}, writes[1])
	assert.Equal(t, withCRC([]byte{0x01, 0x03, 0x01, 0xF6, 0x00, 0x0C}), writes[2])
	assert.Equal(t, writes[1], writes[3])
}

func TestSEN0657_Read_reassemblesChunkedReplies(t *testing.T) {
	dev := newFakeModbusDevice(sen0657Regs())
	dev.chunk = 1

	s, err := newSEN0657(dev).Read(context.Background())
	require.NoError(t, err)
	assert.InDelta(t, 65.8, s.HumidityPct, 1e-9)
}

func TestSEN0657_Read_dropsOutOfRangeValues(t *testing.T) {
	regs := sen0657Regs()
	regs[0x01F8] = 1200 // 120 %RH
	regs[0x01FD] = 85   // 85 hPa: a wrong pressureScale
	dev := newFakeModbusDevice(regs)

	s, err := newSEN0657(dev).Read(context.Background())
	require.NoError(t, err)
	assert.Zero(t, s.Has&weather.FieldHumidity)
	assert.Zero(t, s.Has&weather.FieldPressure)
	assert.NotZero(t, s.Has&weather.FieldTemperature)
}

func TestSEN0657_Read_pressureScale(t *testing.T) {
	regs := sen0657Regs()
	regs[0x01FD] = 8510 // hPa x10
	dev := newFakeModbusDevice(regs)
	sensor := weather.NewSEN0657(dev, weather.SEN0657Options{Wait: noWait, PressureScale: 0.1})

	s, err := sensor.Read(context.Background())
	require.NoError(t, err)
	assert.InDelta(t, 851, s.PressureHPa, 1e-9)
}

func TestSEN0657_Read_directionNorthWraps(t *testing.T) {
	regs := sen0657Regs()
	regs[0x01F7] = 360
	dev := newFakeModbusDevice(regs)

	s, err := newSEN0657(dev).Read(context.Background())
	require.NoError(t, err)
	assert.Zero(t, s.WindDirectionDeg)
	assert.NotZero(t, s.Has&weather.FieldWindDirection)
}

func TestSEN0657_Read_errors(t *testing.T) {
	tests := []struct {
		name  string
		setup func(d *fakeModbusDevice)
		want  error
	}{
		{"no reply", func(d *fakeModbusDevice) { d.silent = true }, weather.ErrTimeout},
		{"bad checksum", func(d *fakeModbusDevice) { d.corrupt = true }, weather.ErrCRC},
		{"read error", func(d *fakeModbusDevice) { d.readErr = errInjected }, errInjected},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dev := newFakeModbusDevice(sen0657Regs())
			tc.setup(dev)

			clock := newFakeClock()
			clock.step = 100 * time.Millisecond
			sensor := weather.NewSEN0657(dev, weather.SEN0657Options{Wait: noWait, Now: clock.Now})

			_, err := sensor.Read(context.Background())
			require.ErrorIs(t, err, tc.want)
		})
	}
}

func TestSEN0657_Read_wrongAddress(t *testing.T) {
	dev := newFakeModbusDevice(sen0657Regs())
	dev.replyAddr = 9

	_, err := newSEN0657(dev).Read(context.Background())
	require.ErrorContains(t, err, "address 9")
}

func TestSEN0657_Read_canceled(t *testing.T) {
	dev := newFakeModbusDevice(sen0657Regs())
	dev.silent = true

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := newSEN0657(dev).Read(ctx)
	require.ErrorIs(t, err, context.Canceled)
}

func TestSEN0657_Read_flushesBeforeEachRequest(t *testing.T) {
	dev := newFakeModbusDevice(sen0657Regs())
	sensor := newSEN0657(dev)

	for range 2 {
		_, err := sensor.Read(context.Background())
		require.NoError(t, err)
	}

	assert.Equal(t, 2, dev.flushes, "a late reply to one poll must not be read as the next")
}

func TestSEN0657_Close(t *testing.T) {
	dev := newFakeModbusDevice(sen0657Regs())
	require.NoError(t, newSEN0657(dev).Close())
	assert.True(t, dev.closed)
}
