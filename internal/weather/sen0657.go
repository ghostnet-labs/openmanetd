package weather

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"
)

// SEN0657 register map (DFRobot wiki, "Reference"). All registers are 16
// bits; temperature is two's complement. 0x01F5 is not documented, so a
// device that rejects it with an exception is read in two parts.
const (
	regWindSpeed  uint16 = 0x01F4 // m/s x10
	regDirection  uint16 = 0x01F7 // degrees, 0 north, clockwise
	regHumidity   uint16 = 0x01F8 // %RH x10
	regTempC      uint16 = 0x01F9 // degC x10, signed
	regPressure   uint16 = 0x01FD // unit unstated; x pressureScale = hPa
	regLightHigh  uint16 = 0x01FE // lux, high 16 bits
	regLightLow   uint16 = 0x01FF // lux, low 16 bits
	regRain       uint16 = 0x0201 // mm x10 since last zeroing
	regFirst             = regWindSpeed
	regLast              = regRain
	regBlockCount        = regLast - regFirst + 1
	// regSecondPart starts the fallback read after the undocumented
	// 0x01F5; it covers the wind sector register through rain.
	regSecondPart uint16 = 0x01F6
	// minRequestGap is the datasheet's minimum time between requests.
	minRequestGap = 200 * time.Millisecond
	tenth         = 0.1
)

// SEN0657Options configures the DFRobot SEN0657 driver.
type SEN0657Options struct {
	// Now defaults to time.Now.
	Now func() time.Time
	// Wait pauses between the two requests of a split read; it defaults
	// to a context-aware timer. Tests pass a no-op.
	Wait func(ctx context.Context, d time.Duration) error
	// Address is the Modbus address (factory default 1).
	Address byte
	// PressureScale multiplies the raw pressure register to get hPa. The
	// datasheet does not state the unit; 1 assumes kPa x10.
	PressureScale float64
	// Timeout bounds one request's reply; it defaults to one second.
	Timeout time.Duration
}

// SEN0657 reads a DFRobot SEN0657 ultrasonic 7-in-1 weather sensor over
// RS485 Modbus-RTU. It is not safe for concurrent use.
type SEN0657 struct {
	client *rtuClient
	closer io.Closer
	wait   func(ctx context.Context, d time.Duration) error
	regs   []uint16
	part   []uint16
	scale  float64
	split  bool
}

// NewSEN0657 returns a driver on port. If port is an io.Closer, Close
// closes it.
func NewSEN0657(port Port, opts SEN0657Options) *SEN0657 {
	if opts.Address == 0 {
		opts.Address = 1
	}

	if opts.PressureScale <= 0 {
		opts.PressureScale = 1
	}

	if opts.Wait == nil {
		opts.Wait = waitCtx
	}

	s := &SEN0657{
		client: newRTUClient(port, opts.Address, opts.Timeout, opts.Now),
		wait:   opts.Wait,
		regs:   make([]uint16, 0, regBlockCount),
		part:   make([]uint16, 0, regBlockCount),
		scale:  opts.PressureScale,
	}

	if c, ok := port.(io.Closer); ok {
		s.closer = c
	}

	return s
}

// Read polls the sensor once.
func (s *SEN0657) Read(ctx context.Context) (Sample, error) {
	regs, err := s.readBlock(ctx)
	if err != nil {
		return Sample{}, err
	}

	return decodeSEN0657(regs, s.scale), nil
}

// readBlock returns registers regFirst..regLast. It reads them in one
// request, and after an exception switches to two requests that skip the
// undocumented register for the rest of the driver's life.
func (s *SEN0657) readBlock(ctx context.Context) ([]uint16, error) {
	if !s.split {
		regs, err := s.client.readHolding(ctx, regFirst, regBlockCount, s.regs)

		var exc *ExceptionError
		if !errors.As(err, &exc) {
			s.regs = regs

			if err != nil {
				return nil, fmt.Errorf("sen0657: %w", err)
			}

			return regs, nil
		}

		s.split = true

		if waitErr := s.wait(ctx, minRequestGap); waitErr != nil {
			return nil, waitErr
		}
	}

	first, err := s.client.readHolding(ctx, regFirst, 1, s.part)
	if err != nil {
		return nil, fmt.Errorf("sen0657: read wind speed: %w", err)
	}

	wind := first[0]

	if err = s.wait(ctx, minRequestGap); err != nil {
		return nil, err
	}

	rest, err := s.client.readHolding(ctx, regSecondPart, regLast-regSecondPart+1, s.part)
	s.part = rest

	if err != nil {
		return nil, fmt.Errorf("sen0657: read registers: %w", err)
	}

	s.regs = append(s.regs[:0], wind, 0)
	s.regs = append(s.regs, rest...)

	return s.regs, nil
}

// Close closes the serial port.
func (s *SEN0657) Close() error {
	if s.closer == nil {
		return nil
	}

	if err := s.closer.Close(); err != nil {
		return fmt.Errorf("sen0657: close: %w", err)
	}

	return nil
}

// decodeSEN0657 converts registers regFirst..regLast to a Sample,
// dropping values outside the sensor's range.
func decodeSEN0657(regs []uint16, pressureScale float64) Sample {
	var s Sample

	if len(regs) < int(regBlockCount) {
		return s
	}

	at := func(reg uint16) uint16 { return regs[reg-regFirst] }

	s.set(FieldWindSpeed, &s.WindSpeedMS, float64(at(regWindSpeed))*tenth, 0, maxWindSpeedMS)
	s.set(FieldWindDirection, &s.WindDirectionDeg, float64(at(regDirection)), 0, maxDirectionDeg)
	s.set(FieldHumidity, &s.HumidityPct, float64(at(regHumidity))*tenth, 0, maxHumidityPct)
	s.set(FieldTemperature, &s.TemperatureC, float64(int16(at(regTempC)))*tenth, minTemperatureC, maxTemperatureC) //nolint:gosec // two's complement by design
	s.set(FieldPressure, &s.PressureHPa, float64(at(regPressure))*pressureScale, minPressureHPa, maxPressureHPa)
	s.set(FieldLight, &s.LightLux, float64(uint32(at(regLightHigh))<<16|uint32(at(regLightLow))), 0, maxLightLux)
	s.set(FieldRainTotal, &s.RainTotalMM, float64(at(regRain))*tenth, 0, maxRainTotalMM)

	if s.WindDirectionDeg == maxDirectionDeg {
		s.WindDirectionDeg = 0
	}

	return s
}

// String names the driver in logs.
func (s *SEN0657) String() string {
	return "sen0657@" + strconv.Itoa(int(s.client.addr))
}

func waitCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("sen0657: %w", ctx.Err())
	case <-t.C:
		return nil
	}
}
