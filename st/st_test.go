package st

// NOTE: this test uses actual hardware on your local network! It does not use mocked-out hardware,
// and will fail if you don't have a motor controller at the IP address 10.10.10.10.

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/resource"
)

const stepsPerRev = 20000

func getDefaultConfig() *Config {
	return &Config{
		Uri:                 "10.10.10.10:7776",
		Protocol:            "ip",
		MinRpm:              0,
		MaxRpm:              900,
		ConnectTimeout:      1,
		StepsPerRev:         stepsPerRev,
		DefaultAcceleration: 100,
		DefaultDeceleration: 100,
	}
}

func getMotorForTesting(t *testing.T, config *Config) (context.Context, *st, error) {
	ctx := context.TODO()
	logger := logging.NewTestLogger(t)
	logger.WithOptions()
	resourceConf := resource.Config{ConvertedAttributes: config}
	m, e := newMotor(ctx, nil, resourceConf, logger)

	// unwrap motor.Motor into st so we can access some non-interface members
	st, _ := m.(*st)
	return ctx, st, e
}

func TestMotorIsMoving(t *testing.T) {
	ctx, motor, err := getMotorForTesting(t, getDefaultConfig())
	assert.Nil(t, err, "failed to construct motor")
	defer motor.Close(ctx)

	isMoving, err := motor.IsMoving(ctx)
	assert.Nil(t, err, "failed to get motor status")
	assert.False(t, isMoving, "motor should be stopped")
	done := make(chan bool)
	go func() {
		err = motor.GoFor(ctx, 600, 10, nil)
		assert.Nil(t, err, "error executing move command")
		close(done)
	}()
	// Sleep a bit to let the motor get going
	time.Sleep(50 * time.Millisecond)

	isMoving, err = motor.IsMoving(ctx)
	assert.Nil(t, err, "failed to get motor status")
	assert.True(t, isMoving, "motor should be moving")

	<-done // Wait for the motor to stop before going to the next test.
}

func TestStatusFunctions(t *testing.T) {
	ctx, motor, err := getMotorForTesting(t, getDefaultConfig())
	assert.Nil(t, err, "failed to construct motor")
	defer motor.Close(ctx)

	status, err := motor.getStatus(ctx)
	assert.Nil(t, err, "failed to get motor status")
	inPosition, err := inPosition(status)
	assert.Nil(t, err, "failed to get in position from status")
	assert.True(t, inPosition, "expected motor to be in position, status %#v", status)
	isMoving, err := motor.IsMoving(ctx)
	assert.Nil(t, err, "failed to get is moving from status")
	assert.False(t, isMoving, "expected motor to be stopped, status %#v", status)

	bufferSize, err := motor.getBufferStatus(ctx)
	assert.Nil(t, err, "failed to get buffer status")
	assert.Equal(t, 63, bufferSize, "buffer is not empty")
}

func TestGoFor(t *testing.T) {
	ctx, motor, err := getMotorForTesting(t, getDefaultConfig())
	assert.Nil(t, err, "failed to construct motor")
	defer motor.Close(ctx)

	err = motor.GoFor(ctx, 600, .001, nil)
	assert.Nil(t, err, "error executing move command")

	err = motor.GoFor(ctx, 600, -.001, nil)
	assert.Nil(t, err, "error executing move command")
}

func TestGoTo(t *testing.T) {
	ctx, motor, err := getMotorForTesting(t, getDefaultConfig())
	assert.Nil(t, err, "failed to construct motor")
	defer motor.Close(ctx)

	// First reset the position to 0
	err = motor.ResetZeroPosition(ctx, 0, nil)
	assert.Nil(t, err, "error resetting position")

	err = motor.GoTo(ctx, 100, .001, nil)
	assert.Nil(t, err, "error executing move command")

	position, err := motor.Position(ctx, nil)
	assert.Nil(t, err, "error getting position")
	expectedSteps := .001
	assert.Equal(t, expectedSteps, position, "position should be equal to %v", expectedSteps)

	err = motor.GoTo(ctx, 100, .01, nil)
	assert.Nil(t, err, "error executing move command")

	position, err = motor.Position(ctx, nil)
	assert.Nil(t, err, "error getting position")
	expectedSteps = .01
	assert.Equal(t, expectedSteps, position, "position should be equal to %v", expectedSteps)
}

func TestPosition(t *testing.T) {
	distance := 0.1 // revolutions to travel
	ctx, motor, err := getMotorForTesting(t, getDefaultConfig())
	assert.Nil(t, err, "failed to construct motor")
	defer motor.Close(ctx)

	// First reset the position to 0
	err = motor.ResetZeroPosition(ctx, 0, nil)
	assert.Nil(t, err, "error resetting position")

	position, err := motor.Position(ctx, nil)
	assert.Nil(t, err, "error getting position")
	assert.Equal(t, 0.0, position, "position should be 0")

	// Move the motor a bit
	err = motor.GoFor(ctx, 600, distance, nil)
	assert.Nil(t, err, "error executing move command")

	// Check the position again
	position, err = motor.Position(ctx, nil)
	assert.Nil(t, err, "error getting position")
	assert.Equal(t, distance, position, "position should be equal to %v", distance)

	// Move the motor a bit, but this time, backwards
	err = motor.GoFor(ctx, 600, -distance, nil)
	assert.Nil(t, err, "error executing move command")

	// Check the position again
	position, err = motor.Position(ctx, nil)
	assert.Nil(t, err, "error getting position")
	assert.Equal(t, 0.0, position, "position should be equal to 0")

	// Reset the position to a nonzero value
	err = motor.ResetZeroPosition(ctx, 1, nil)
	assert.Nil(t, err, "error resetting position")

	position, err = motor.Position(ctx, nil)
	assert.Nil(t, err, "error getting position")
	assert.Equal(t, -1.0, position)

	err = motor.GoFor(ctx, 600, 1, nil)
	assert.Nil(t, err, "error executing move command")

	position, err = motor.Position(ctx, nil)
	assert.Nil(t, err, "error getting position")
	assert.Equal(t, 0.0, position)
}

func TestDoCommand(t *testing.T) {
	ctx, motor, err := getMotorForTesting(t, getDefaultConfig())
	assert.Nil(t, err, "failed to construct motor")
	defer motor.Close(ctx)

	_, err = motor.DoCommand(ctx, map[string]interface{}{"command": "DI20000"})
	assert.Nil(t, err, "error executing do command")
	_, err = motor.DoCommand(ctx, map[string]interface{}{"command": "VE1"})
	assert.Nil(t, err, "error executing do command")
	_, err = motor.DoCommand(ctx, map[string]interface{}{"command": "AC100"})
	assert.Nil(t, err, "error executing do command")
	_, err = motor.DoCommand(ctx, map[string]interface{}{"command": "DE100"})
	assert.Nil(t, err, "error executing do command")
	resp, err := motor.DoCommand(ctx, map[string]interface{}{"command": "FL"})
	assert.Nil(t, err, "error executing do command")
	assert.NotNil(t, resp["response"], "response should not be nil")
}

func TestAccelOverrides(t *testing.T) {
	ctx, motor, err := getMotorForTesting(t, getDefaultConfig())
	assert.Nil(t, err, "failed to construct motor")
	defer motor.Close(ctx)

	// Since we're moving a real motor, we can use real time to see how fast it's going.
	t1 := time.Now()
	err = motor.GoFor(ctx, 600, 5, nil)
	assert.Nil(t, err, "error moving motor at default acceleration")
	t2 := time.Now()
	err = motor.GoFor(ctx, 600, 5, map[string]interface{}{"acceleration": 10.0})
	assert.Nil(t, err, "error moving motor at slower acceleration")
	t3 := time.Now()
	err = motor.GoFor(ctx, 600, 5, map[string]interface{}{"deceleration": 10.0})
	assert.Nil(t, err, "error moving motor at slower deceleration")
	t4 := time.Now()

	assert.Greater(t, t3.Sub(t2), 2*t1.Sub(t2)) // Slow acceleration takes longer than default
	assert.Greater(t, t4.Sub(t3), 2*t1.Sub(t2)) // Slow deceleration takes longer than default, too
}

func timeRevolution(
	t *testing.T, config *Config, rpm float64, description string, extra map[string]interface{},
) time.Duration {
	t.Helper()
	ctx, motor, err := getMotorForTesting(t, config)
	assert.Nil(t, err, "failed to construct motor")
	defer motor.Close(ctx)

	start := time.Now()
	err = motor.GoFor(ctx, rpm, 1, extra)
	assert.Nil(t, err, description)
	end := time.Now()
	return end.Sub(start)
}

func assertApproximatelyEqual(t *testing.T, a, b time.Duration, message string) {
	t.Helper()
	tolerance := 0.07 // Fraction of values that can differ
	assert.Greater(t, time.Duration((1+tolerance)*float64(a)), b, message)
	assert.Greater(t, time.Duration((1+tolerance)*float64(b)), a, message)
}

func TestAccelLimits(t *testing.T) {
	conf := getDefaultConfig()
	defaultTime := timeRevolution(t, conf, 600, "default config", nil)

	// If you try to set accel/decel values out of range, clamp it to the min/max.

	conf = getDefaultConfig() // Reset anything changed in a previous test
	conf.MinAcceleration = 100
	clampedMinAccelTime := timeRevolution(t, conf, 600, "setting acceleration below minimum",
		map[string]interface{}{"acceleration": 10.0})
	assertApproximatelyEqual(t, defaultTime, clampedMinAccelTime, "acceleration below minimum")

	conf = getDefaultConfig()
	conf.MaxAcceleration = 100
	clampedMaxAccelTime := timeRevolution(t, conf, 600, "setting acceleration above maximum",
		map[string]interface{}{"acceleration": 200.0})
	assertApproximatelyEqual(t, defaultTime, clampedMaxAccelTime, "acceleration above maximum")

	conf = getDefaultConfig()
	conf.MinDeceleration = 100
	clampedMinDecelTime := timeRevolution(t, conf, 600, "setting deceleration below minimum",
		map[string]interface{}{"deceleration": 10.0})
	assertApproximatelyEqual(t, defaultTime, clampedMinDecelTime, "deceleration below minimum")

	conf = getDefaultConfig()
	conf.MaxDeceleration = 100
	clampedMaxDecelTime := timeRevolution(t, conf, 600, "setting deceleration above maximum",
		map[string]interface{}{"deceleration": 200.0})
	assertApproximatelyEqual(t, defaultTime, clampedMaxDecelTime, "deceleration above maximum")

	// but clamping shouldn't affect values within the valid range!
	conf = getDefaultConfig()
	conf.MinAcceleration = 1
	conf.MinDeceleration = 1
	unclampedMinAccelTime := timeRevolution(t, conf, 600, "setting acceleration below minimum",
		map[string]interface{}{"acceleration": 10.0})
	assert.Greater(t, unclampedMinAccelTime, 2*defaultTime)
	unclampedMinDecelTime := timeRevolution(t, conf, 600, "setting deceleration below minimum",
		map[string]interface{}{"deceleration": 10.0})
	assert.Greater(t, unclampedMinDecelTime, 2*defaultTime)

	// Increasing the maximum acceleration above 100 rev/sec^2 doesn't seem to appreciably affect
	// the total move time. So instead, let's lower the default acceleration and make sure that you
	// can set it fast again anyway.
	conf = getDefaultConfig()
	conf.DefaultAcceleration = 10
	conf.DefaultDeceleration = 10
	slowAccelDefaultTime := timeRevolution(t, conf, 600, "slow accel config", nil)

	conf = getDefaultConfig()
	conf.DefaultAcceleration = 10
	conf.MaxAcceleration = 200
	unclampedMaxAccelTime := timeRevolution(t, conf, 600, "setting acceleration above maximum",
		map[string]interface{}{"acceleration": 100.0})
	assert.Greater(t, slowAccelDefaultTime, 2*unclampedMaxAccelTime)

	conf = getDefaultConfig()
	conf.DefaultDeceleration = 10
	conf.MaxDeceleration = 200
	unclampedMaxDecelTime := timeRevolution(t, conf, 600, "setting deceleration above maximum",
		map[string]interface{}{"deceleration": 100.0})
	assert.Greater(t, slowAccelDefaultTime, 2*unclampedMaxDecelTime)
}

func TestRpmLimits(t *testing.T) {
	conf := getDefaultConfig()
	defaultTime := timeRevolution(t, conf, 600, "default config", nil)

	// If you try to set RPM values out of range, clamp it to the min/max.
	conf = getDefaultConfig()
	conf.MinRpm = 600
	clampedMinRpmTime := timeRevolution(t, conf, 100, "setting rpm below minimum", nil)
	assertApproximatelyEqual(t, defaultTime, clampedMinRpmTime, "rpm below minimum")

	conf = getDefaultConfig()
	conf.MaxRpm = 600
	clampedMaxRpmTime := timeRevolution(t, conf, 1000, "setting rpm above maximum", nil)
	assertApproximatelyEqual(t, defaultTime, clampedMaxRpmTime, "rpm above maximum")

	// but if you're in the right range, it's okay to go that speed.
	conf = getDefaultConfig()
	conf.MinRpm = 10
	conf.MaxRpm = 1000
	slowRpmTime := timeRevolution(t, conf, 100, "setting rpm slower", nil)
	assert.Greater(t, slowRpmTime, 2*defaultTime)
}
