package app

import (
	"time"

	"github.com/indiefan/home_assistant_nanit/pkg/baby"
	"github.com/indiefan/home_assistant_nanit/pkg/client"
	"github.com/indiefan/home_assistant_nanit/pkg/utils"
	"github.com/rs/zerolog/log"
)

func processSensorData(babyUID string, sensorData []*client.SensorData, stateManager *baby.StateManager) {
	// Parse sensor update
	stateUpdate := baby.State{}
	for _, sensorDataSet := range sensorData {
		if *sensorDataSet.SensorType == client.SensorType_TEMPERATURE {
			stateUpdate.SetTemperatureMilli(*sensorDataSet.ValueMilli)
		}
		if *sensorDataSet.SensorType == client.SensorType_HUMIDITY {
			stateUpdate.SetHumidityMilli(*sensorDataSet.ValueMilli)
		}
		if *sensorDataSet.SensorType == client.SensorType_NIGHT {
			stateUpdate.SetIsNight(*sensorDataSet.Value == 1)
		}
	}

	stateManager.Update(babyUID, stateUpdate)
}

func requestLocalStreaming(babyUID string, targetURL string, streamingStatus client.Streaming_Status, conn *client.WebsocketConnection, stateManager *baby.StateManager) {
	for {
		switch streamingStatus {
		case client.Streaming_STARTED:
			log.Info().Str("target", targetURL).Msg("Requesting local streaming")
		case client.Streaming_PAUSED:
			log.Info().Str("target", targetURL).Msg("Pausing local streaming")
		case client.Streaming_STOPPED:
			log.Info().Str("target", targetURL).Msg("Stopping local streaming")
		}

		awaitResponse := conn.SendRequest(client.RequestType_PUT_STREAMING, &client.Request{
			Streaming: &client.Streaming{
				Id:       client.StreamIdentifier(client.StreamIdentifier_MOBILE).Enum(),
				RtmpUrl:  utils.ConstRefStr(targetURL),
				Status:   client.Streaming_Status(streamingStatus).Enum(),
				Attempts: utils.ConstRefInt32(1),
			},
		})

		_, err := awaitResponse(30 * time.Second)

		if err != nil {
			if err.Error() == "Forbidden: Number of Mobile App connections above limit, declining connection" {
				log.Warn().Err(err).Msg("Too many app connections, waiting for local connection to become available...")
				stateManager.Update(babyUID, *baby.NewState().SetStreamRequestState(baby.StreamRequestState_RequestFailed))
				time.Sleep(300 * time.Second)
				continue
			} else if err.Error() != "Request timeout" {
				if stateManager.GetBabyState(babyUID).GetStreamState() == baby.StreamState_Alive {
					log.Info().Err(err).Msg("Failed to request local streaming, but stream seems to be alive from previous run")
				} else if stateManager.GetBabyState(babyUID).GetStreamState() == baby.StreamState_Unhealthy {
					log.Error().Err(err).Msg("Failed to request local streaming and stream seems to be dead")
					stateManager.Update(babyUID, *baby.NewState().SetStreamRequestState(baby.StreamRequestState_RequestFailed))
				} else {
					log.Warn().Err(err).Msg("Failed to request local streaming, awaiting stream health check")
					stateManager.Update(babyUID, *baby.NewState().SetStreamRequestState(baby.StreamRequestState_RequestFailed))
				}

				return
			}

			if !stateManager.GetBabyState(babyUID).GetIsWebsocketAlive() {
				return
			}

			log.Warn().Msg("Streaming request timeout, trying again")

		} else {
			log.Info().Msg("Local streaming successfully requested")
			stateManager.Update(babyUID, *baby.NewState().SetStreamRequestState(baby.StreamRequestState_Requested))
			return
		}
	}
}

func processLight(babyUID string, control *client.Control, stateManager *baby.StateManager) {
	if control.NightLight != nil {
		stateUpdate := baby.State{}
		stateUpdate.SetNightLight(*control.NightLight == client.Control_LIGHT_ON)
		stateManager.Update(babyUID, stateUpdate)
	}
}

func sendLightCommand(nightLightState bool, conn *client.WebsocketConnection) {
	nightLight := client.Control_LIGHT_OFF
	if nightLightState {
		nightLight = client.Control_LIGHT_ON
	}
	conn.SendRequest(client.RequestType_PUT_CONTROL, &client.Request{
		Control: &client.Control{
			NightLight: &nightLight,
		},
	})
}

func processStandby(babyUID string, settings *client.Settings, stateManager *baby.StateManager) {
	if settings.SleepMode != nil {
		stateUpdate := baby.State{}
		stateUpdate.SetStandby(*settings.SleepMode)
		stateManager.Update(babyUID, stateUpdate)
	}
}

func sendStandbyCommand(standbyState bool, conn *client.WebsocketConnection) {
	conn.SendRequest(client.RequestType_PUT_SETTINGS, &client.Request{
		Settings: &client.Settings{
			SleepMode: &standbyState,
		},
	})
}
// sendLightCommandWithReset - sends light command with reset logic if enabled
func (app *App) sendLightCommandWithReset(babyUID string, enabled bool, conn *client.WebsocketConnection) bool {
	if !app.Opts.WebSocketReset.Enabled {
		// If reset is disabled, use the original function
		sendLightCommand(enabled, conn)
		return true
	}

	// Try to send the command with timeout
	success := app.sendLightCommandWithTimeout(babyUID, enabled, conn)
	
	if !success && app.shouldAttemptReset(babyUID) {
		log.Warn().
			Str("baby_uid", babyUID).
			Bool("enabled", enabled).
			Msg("Light command failed, attempting WebSocket reset")
		
		// Add command to pending retries
		app.addPendingRetry(babyUID, "light", enabled)
		
		// Force reconnection
		manager := app.getBabyManager(babyUID)
		if manager != nil {
			manager.ForceReconnect()
		}
		
		return false
	}
	
	return success
}

// sendStandbyCommandWithReset - sends standby command with reset logic if enabled
func (app *App) sendStandbyCommandWithReset(babyUID string, enabled bool, conn *client.WebsocketConnection) bool {
	if !app.Opts.WebSocketReset.Enabled {
		// If reset is disabled, use the original function
		sendStandbyCommand(enabled, conn)
		return true
	}

	// Try to send the command with timeout
	success := app.sendStandbyCommandWithTimeout(babyUID, enabled, conn)
	
	if !success && app.shouldAttemptReset(babyUID) {
		log.Warn().
			Str("baby_uid", babyUID).
			Bool("enabled", enabled).
			Msg("Standby command failed, attempting WebSocket reset")
		
		// Add command to pending retries
		app.addPendingRetry(babyUID, "standby", enabled)
		
		// Force reconnection
		manager := app.getBabyManager(babyUID)
		if manager != nil {
			manager.ForceReconnect()
		}
		
		return false
	}
	
	return success
}

// sendLightCommandWithTimeout - sends light command with timeout checking
func (app *App) sendLightCommandWithTimeout(babyUID string, enabled bool, conn *client.WebsocketConnection) bool {
	nightLight := client.Control_LIGHT_OFF
	if enabled {
		nightLight = client.Control_LIGHT_ON
	}
	
	awaitResponse := conn.SendRequest(client.RequestType_PUT_CONTROL, &client.Request{
		Control: &client.Control{
			NightLight: &nightLight,
		},
	})
	
	// Wait for camera response with configured timeout
	response, err := awaitResponse(app.Opts.WebSocketReset.CommandTimeout)
	if err != nil {
		log.Error().
			Err(err).
			Str("baby_uid", babyUID).
			Bool("enabled", enabled).
			Dur("timeout", app.Opts.WebSocketReset.CommandTimeout).
			Msg("Failed to send light command within timeout")
		return false
	}
	
	if response.StatusCode != nil && *response.StatusCode != 200 {
		log.Warn().
			Int32("status_code", *response.StatusCode).
			Str("status_message", response.GetStatusMessage()).
			Str("baby_uid", babyUID).
			Bool("enabled", enabled).
			Msg("Light command failed with error status")
		return false
	}
	
	log.Debug().
		Str("baby_uid", babyUID).
		Bool("enabled", enabled).
		Msg("Light command sent successfully")
	return true
}

// sendStandbyCommandWithTimeout - sends standby command with timeout checking
func (app *App) sendStandbyCommandWithTimeout(babyUID string, enabled bool, conn *client.WebsocketConnection) bool {
	awaitResponse := conn.SendRequest(client.RequestType_PUT_SETTINGS, &client.Request{
		Settings: &client.Settings{
			SleepMode: &enabled,
		},
	})
	
	// Wait for camera response with configured timeout
	response, err := awaitResponse(app.Opts.WebSocketReset.CommandTimeout)
	if err != nil {
		log.Error().
			Err(err).
			Str("baby_uid", babyUID).
			Bool("enabled", enabled).
			Dur("timeout", app.Opts.WebSocketReset.CommandTimeout).
			Msg("Failed to send standby command within timeout")
		return false
	}
	
	if response.StatusCode != nil && *response.StatusCode != 200 {
		log.Warn().
			Int32("status_code", *response.StatusCode).
			Str("status_message", response.GetStatusMessage()).
			Str("baby_uid", babyUID).
			Bool("enabled", enabled).
			Msg("Standby command failed with error status")
		return false
	}
	
	log.Debug().
		Str("baby_uid", babyUID).
		Bool("enabled", enabled).
		Msg("Standby command sent successfully")
	return true
}
