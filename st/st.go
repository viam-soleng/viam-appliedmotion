package st

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.uber.org/multierr"
	"go.viam.com/rdk/components/motor"
	"go.viam.com/rdk/logging"
	"go.viam.com/rdk/resource"

	"viam-labs/viam-appliedmotion/common"
)

var Model = resource.NewModel("viam-labs", "appliedmotion", "st")

type st struct {
	resource.Named
	mu          sync.RWMutex
	logger      logging.Logger
	cancelCtx   context.Context
	cancelFunc  func()
	comm        commPort
	stepsPerRev int64

	accelLimits limits
	decelLimits limits
	rpmLimits   limits

	defaultAccel float64
	defaultDecel float64
}

var ErrStatusMessageIncorrectLength = errors.New("status message incorrect length")

// Investigate:
// CE - Communication Error

func init() {
	resource.RegisterComponent(
		motor.API,
		Model,
		resource.Registration[motor.Motor, *Config]{Constructor: newMotor})
}

func newMotor(ctx context.Context, deps resource.Dependencies, conf resource.Config, logger logging.Logger) (motor.Motor, error) {
	logger.Infof("Starting Applied Motion Products ST Motor Driver %s", common.Version)
	cancelCtx, cancelFunc := context.WithCancel(context.Background())

	s := st{
		Named:      conf.ResourceName().AsNamed(),
		logger:     logger,
		cancelCtx:  cancelCtx,
		cancelFunc: cancelFunc,
		mu:         sync.RWMutex{},
	}

	if err := s.Reconfigure(ctx, deps, conf); err != nil {
		return nil, err
	}
	return &s, nil
}

func (s *st) Reconfigure(ctx context.Context, _ resource.Dependencies, conf resource.Config) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logger.Debug("Reconfiguring Applied Motion Products ST Motor Driver")

	newConf, err := resource.NativeConfig[*Config](conf)
	if err != nil {
		return err
	}

	// In case the module has changed name
	s.Named = conf.ResourceName().AsNamed()

	// Update the steps per rev
	s.stepsPerRev = newConf.StepsPerRev

	// If we have an old comm object, shut it down. We'll set it up again next paragraph.
	if s.comm != nil {
		s.comm.Close()
		s.comm = nil
	}

	if comm, err := getComm(s.cancelCtx, newConf, s.logger); err != nil {
		return err
	} else {
		s.comm = comm
	}

	s.accelLimits = newLimits("acceleration", newConf.MinAcceleration, newConf.MaxAcceleration)
	s.decelLimits = newLimits("deceleration", newConf.MinDeceleration, newConf.MaxDeceleration)
	s.rpmLimits = newLimits("rpm", newConf.MinRpm, newConf.MaxRpm)

	s.defaultAccel = newConf.DefaultAcceleration
	if s.defaultAccel > 0 {
		if err := s.comm.store(ctx, "AC", s.defaultAccel); err != nil {
			return err
		}
	}

	s.defaultDecel = newConf.DefaultDeceleration
	if s.defaultDecel > 0 {
		if err := s.comm.store(ctx, "DE", s.defaultDecel); err != nil {
			return err
		}
		// Set the maximum deceleration when stopping a move in the middle, too.
		if err := s.comm.store(ctx, "AM", s.defaultDecel); err != nil {
			return err
		}
	}

	return nil
}

func getComm(ctx context.Context, conf *Config, logger logging.Logger) (commPort, error) {
	switch {
	case strings.ToLower(conf.Protocol) == "can":
		return nil, fmt.Errorf("unsupported comm type %s", conf.Protocol)
	case strings.ToLower(conf.Protocol) == "ip":
		logger.Debug("Creating IP Comm Port")
		if conf.ConnectTimeout == 0 {
			logger.Debug("Setting default connect timeout to 5 seconds")
			conf.ConnectTimeout = 5
		}
		timeout := time.Duration(conf.ConnectTimeout * int64(time.Second))
		return newIpComm(ctx, conf.Uri, timeout, logger)
	case strings.ToLower(conf.Protocol) == "rs485":
		logger.Debug("Creating RS485 Comm Port")
		return newSerialComm(ctx, conf.Uri, logger)
	case strings.ToLower(conf.Protocol) == "rs232":
		logger.Debug("Creating RS232 Comm Port")
		return newSerialComm(ctx, conf.Uri, logger)
	default:
		return nil, fmt.Errorf("unknown comm type %s", conf.Protocol)
	}
}

func (s *st) stopMovement(ctx context.Context) error {
	// The only movement we might be in the middle of is continuous jogging from a SetPower.
	// Naively, SJ should stop jogging and thus stop continuous movement. However, if you're
	// jogging, then call SJ, then do a non-jogging movement (e.g., FL) and that movement
	// completes, it resumes jogging for reasons Alan doesn't understand. The SK command stops and
	// clears the queue, and then we don't re-commence jogging later.
	_, err := s.comm.send(ctx, "SK")
	return err
}

func (s *st) getStatus(ctx context.Context) ([]byte, error) {
	if resp, err := s.comm.send(ctx, "SC"); err != nil {
		return nil, err
	} else {
		// TODO: document this better, once you've read the manual.

		// Response format: "SC=0009{63"
		// we need to strip off the command and any leading or trailing stuff
		startIndex := strings.Index(resp, "=")
		if startIndex == -1 {
			return nil, fmt.Errorf("unable to find response data in %v", resp)
		}
		endIndex := strings.Index(resp, "{")
		if endIndex == -1 {
			endIndex = startIndex + 5
		}

		resp = resp[startIndex+1 : endIndex]
		if val, err := hex.DecodeString(resp); err != nil {
			return nil, err
		} else {
			if len(val) != 2 {
				return nil, ErrStatusMessageIncorrectLength
			}
			return val, nil
		}
	}
}

func inPosition(status []byte) (bool, error) {
	if len(status) != 2 {
		return false, ErrStatusMessageIncorrectLength
	}
	return (status[1]>>3)&1 == 1, nil
}

func (s *st) getBufferStatus(ctx context.Context) (int, error) {
	if resp, err := s.comm.send(ctx, "BS"); err != nil {
		return -1, err
	} else {
		// TODO: document this better. The current comment doesn't match the code.
		// The response should look something like BS=<num>
		startIndex := strings.Index(resp, "=")
		if startIndex == -1 {
			return -1, fmt.Errorf("unable to find response data in %v", resp)
		}
		endIndex := strings.Index(resp, "{")
		if endIndex == -1 {
			endIndex = startIndex + 3
		}

		if endIndex > len(resp) {
			return 0, fmt.Errorf("unexpected response length %v", resp)
		}

		resp = resp[startIndex+1 : endIndex]
		return strconv.Atoi(resp)
	}
}

func (s *st) waitForMoveCommandToComplete(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			// We need to stop the hardware when our context is canceled. Sending the stop needs a
			// non-canceled context, and we cannot use ctx since that has already been canceled.
			// Fortunately, stopping should be very fast and not block, so it's alright to use the
			// background context for this.
			s.Stop(context.Background(), nil)
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
		if bufferIsEmpty, err := s.isBufferEmpty(ctx); err != nil {
			return err
		} else {
			if isMoving, err := s.IsMoving(ctx); err != nil {
				return err
			} else {
				if bufferIsEmpty && !isMoving {
					return nil
				}
			}
		}
	}
}

func (s *st) isBufferEmpty(ctx context.Context) (bool, error) {
	b, e := s.getBufferStatus(ctx)
	return b == 63, e
}

func (s *st) Close(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return multierr.Combine(s.stopMovement(ctx),
		s.comm.Close())
}

func (s *st) GoFor(ctx context.Context, rpm float64, positionRevolutions float64, extra map[string]interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logger.Debugf("GoFor: rpm=%v, positionRevolutions=%v, extra=%v", rpm, positionRevolutions, extra)

	// The speed we send to the motor controller must always be positive. If it comes in negative,
	// flip the distance to travel.
	if rpm < 0 {
		rpm *= -1
		positionRevolutions *= -1
	}

	// Send the configuration commands to setup the motor for the move
	return s.configuredMove(ctx, "FL", positionRevolutions, rpm, extra)
}

func (s *st) GoTo(ctx context.Context, rpm float64, positionRevolutions float64, extra map[string]interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// FP?
	// For Ethernet drives, do not use FP with a position parameter. Instead, use DI to set the target position.
	// I guess this means run:
	// 	DI8000
	// 	FP
	s.logger.Debugf("GoTo: rpm=%v, positionRevolutions=%v, extra=%v", rpm, positionRevolutions, extra)

	// Send the configuration commands to setup the motor for the move
	return s.configuredMove(ctx, "FP", positionRevolutions, rpm, extra)
}

func (s *st) SetRPM(ctx context.Context, rpm float64, extra map[string]interface{}) error {
	powerLevel := rpm / s.rpmLimits.max
	return s.SetPower(ctx, powerLevel, extra)
}

func (s *st) configuredMove(
	ctx context.Context,
	command string,
	positionRevolutions, rpm float64,
	extra map[string]interface{},
) error {
	if err := s.stopMovement(ctx); err != nil {
		return err
	}

	if val, exists := extra["acceleration"]; exists {
		if valFloat, ok := val.(float64); ok {
			extra["acceleration"] = s.accelLimits.Bound(valFloat, s.logger)
		}
	}
	if val, exists := extra["deceleration"]; exists {
		if valFloat, ok := val.(float64); ok {
			extra["deceleration"] = s.decelLimits.Bound(valFloat, s.logger)
		}
	}

	oldAcceleration, err := setOverrides(ctx, s.comm, extra)
	if err != nil {
		return err
	}

	rpm = s.rpmLimits.Bound(rpm, s.logger)

	// need to convert from RPM to revs per second
	revSec := rpm / 60
	// need to convert from revs to steps
	positionSteps := int64(positionRevolutions * float64(s.stepsPerRev))
	// Set the distance first
	if _, err := s.comm.send(ctx, fmt.Sprintf("DI%d", positionSteps)); err != nil {
		return err
	}

	// Now set the velocity
	if err := s.comm.store(ctx, "VE", revSec); err != nil {
		return err
	}

	if _, err := s.comm.send(ctx, command); err != nil {
		return err
	}
	return multierr.Combine(s.waitForMoveCommandToComplete(ctx),
		oldAcceleration.restore(ctx, s.comm))
}

func (s *st) IsMoving(ctx context.Context) (bool, error) {
	// If we locked the mutex, we'd block until after any GoFor or GoTo commands were finished! We
	// also aren't mutating any state in the struct itself, so there is no need to lock it.
	s.logger.Debug("IsMoving")
	status, err := s.getStatus(ctx)

	if err != nil {
		return false, err
	}
	if len(status) != 2 {
		return false, ErrStatusMessageIncorrectLength
	}
	return (status[1]>>4)&1 == 1, nil
}

// IsPowered implements motor.Motor.
func (s *st) IsPowered(ctx context.Context, extra map[string]interface{}) (bool, float64, error) {
	// The same as IsMoving, don't lock the mutex.
	s.logger.Debugf("IsPowered: extra=%v", extra)
	status, err := s.getStatus(ctx)
	if err != nil {
		return false, 0, err
	}
	if len(status) != 2 {
		return false, 0, ErrStatusMessageIncorrectLength
	}
	// The second return value is supposed to be the fraction of power sent to the motor, between 0
	// (off) and 1 (maximum power). It's unclear how to implement this for a stepper motor, so we
	// return 0 no matter what.
	return (status[1]&1 == 1), 0, err
}

// Position implements motor.Motor.
func (s *st) Position(ctx context.Context, extra map[string]interface{}) (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logger.Debugf("Position: extra=%v", extra)

	// Use EP if we've got an encoder plugged in (this struct currently doesn't support that).
	// Use IP if we don't have an encoder and want to just count steps.
	// The response should look something like IP=<num>
	if resp, err := s.comm.send(ctx, "IP"); err != nil {
		return 0, err
	} else {
		startIndex := strings.Index(resp, "=")
		if startIndex == -1 {
			return 0, fmt.Errorf("unexpected response %v", resp)
		}
		resp = resp[startIndex+1:]
		if val, err := strconv.ParseUint(resp, 16, 32); err != nil {
			return 0, err
		} else {
			// We parsed the value as though it was unsigned, but it's really signed. We can't
			// parse it as signed originally because strconv expects the sign to be indicated by a
			// "-" at the beginning, not by the most significant bit in the word. Convert it here.
			return float64(int32(val)) / float64(s.stepsPerRev), nil
		}
	}
}

// Properties implements motor.Motor.
func (s *st) Properties(ctx context.Context, extra map[string]interface{}) (motor.Properties, error) {
	return motor.Properties{PositionReporting: true}, nil
}

// ResetZeroPosition implements motor.Motor.
func (s *st) ResetZeroPosition(ctx context.Context, offset float64, extra map[string]interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logger.Debugf("ResetZeroPosition: offset=%v", offset)

	// The driver only has 32 bits of precision. If we go beyond that, we're gonna have a bad time.
	newCurrentPosition := int32(-offset * float64(s.stepsPerRev))

	// The docs indicate that for proper reset, you must send both EP and SP. The EP is only
	// important if we've got an encoder plugged in, though we currently don't support that. If we
	// do start supporting an encoder, we should also use the CI and CC commands to increase the
	// current going to the motor while the encoder is being reset, so the motor can't wiggle
	// around during the reset.

	// First reset the encoder
	if _, err := s.comm.send(ctx, fmt.Sprintf("EP%d", newCurrentPosition)); err != nil {
		return err
	}

	// Then reset the internal position
	if _, err := s.comm.send(ctx, fmt.Sprintf("SP%d", newCurrentPosition)); err != nil {
		return err
	}

	return nil
}

// SetPower implements motor.Motor. We use the Continuous Jogging interface on the motor.
func (s *st) SetPower(ctx context.Context, powerPct float64, extra map[string]interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	// The GoTo and GoFor commands communicate the number of steps the motor should move, but
	// SetPower requires telling the motor the number of revolutions per second the motor should
	// spin at. Consequently, we need to tell it the number of steps per revolution, using the EG
	// command.
	if _, err := s.comm.send(ctx, fmt.Sprintf("EG%d", s.stepsPerRev)); err != nil {
		return err
	}

	acceleration, deceleration, err := convertExtras(extra)
	if err != nil {
		return err
	}

	acceleration = s.accelLimits.Bound(acceleration, s.logger)
	if _, err := s.comm.send(ctx, fmt.Sprintf("JA%f", acceleration)); err != nil {
		return err
	}

	deceleration = s.decelLimits.Bound(deceleration, s.logger)
	if _, err := s.comm.send(ctx, fmt.Sprintf("JL%f", deceleration)); err != nil {
		return err
	}

	// Make sure not to go past the maximum speed
	if powerPct > 1.0 {
		powerPct = 1.0
	}
	if powerPct < -1.0 {
		powerPct = -1.0
	}

	targetRPM := powerPct * s.rpmLimits.max
	if math.Abs(targetRPM) < s.rpmLimits.min {
		return fmt.Errorf("refusing to set power to less than the minimum RPM (%f vs %f)",
			targetRPM, s.rpmLimits.min)
	}
	targetRPS := targetRPM / 60.0 // Revolutions per second, not per minute!

	// You might expect us to use DI to set the direction, JS to set the (unsigned) jogging speed,
	// and then CJ to start continuous jogging. However, if you call SetPower again while we're
	// already jogging, we need to use CS to set the new speed, which should be signed rather than
	// using DI to change direction. This is much simpler if we just start jogging and then
	// immediately set the (signed) velocity.
	if _, err := s.comm.send(ctx, "CJ"); err != nil {
		return err
	}
	if _, err := s.comm.send(ctx, fmt.Sprintf("CS%f", targetRPS)); err != nil {
		return err
	}

	return nil
}

// Stop implements motor.Motor.
func (s *st) Stop(ctx context.Context, extras map[string]interface{}) error {
	// SK - Stop & Kill? Stops and erases queue
	// SM - Stop Move? Stops and leaves queue intact?
	// ST - Halts the current buffered command being executed, but does not affect other buffered commands in the command buffer
	s.logger.Debugf("Stop called with %v", extras)
	_, err := s.comm.send(ctx, "SK") // Stop the current move and clear any queued moves, too.
	if err != nil {
		return err
	}
	return nil
}

func (s *st) DoCommand(ctx context.Context, cmd map[string]interface{}) (map[string]interface{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logger.Debug("DoCommand called with %v", cmd)
	command := cmd["command"].(string)
	response, err := s.comm.send(ctx, command)
	return map[string]interface{}{"response": response}, err
}
