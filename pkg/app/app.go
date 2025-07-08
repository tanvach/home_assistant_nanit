package app

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/indiefan/home_assistant_nanit/pkg/baby"
	"github.com/indiefan/home_assistant_nanit/pkg/client"
	"github.com/indiefan/home_assistant_nanit/pkg/message"
	"github.com/indiefan/home_assistant_nanit/pkg/mqtt"
	"github.com/indiefan/home_assistant_nanit/pkg/rtmpserver"
	"github.com/indiefan/home_assistant_nanit/pkg/session"
	"github.com/indiefan/home_assistant_nanit/pkg/utils"
	"github.com/rs/zerolog/log"
)

// App - application container
type App struct {
	Opts             Opts
	SessionStore     *session.Store
	BabyStateManager *baby.StateManager
	RestClient       *client.NanitClient
	MQTTConnection   *mqtt.Connection
	
	// Connection registry for routing MQTT commands to the correct baby
	babyConnections map[string]*client.WebsocketConnection
	connectionsMutex sync.RWMutex
	
	// WebSocket manager registry for forced reconnection
	babyManagers     map[string]*client.WebsocketConnectionManager
	managersMutex    sync.RWMutex
	
	// Command retry tracking
	pendingRetries   map[string]*commandRetry
	retriesMutex     sync.RWMutex
}

// commandRetry - tracks a command that needs to be retried after reconnection
type commandRetry struct {
	CommandType string // "light" or "standby"
	BabyUID     string
	Enabled     bool
	Timestamp   time.Time
}

// NewApp - constructor
func NewApp(opts Opts) *App {
	sessionStore := session.InitSessionStore(opts.SessionFile)

	instance := &App{
		Opts:             opts,
		BabyStateManager: baby.NewStateManager(),
		SessionStore:     sessionStore,
		RestClient: &client.NanitClient{
			Email:        opts.NanitCredentials.Email,
			Password:     opts.NanitCredentials.Password,
			RefreshToken: opts.NanitCredentials.RefreshToken,
			SessionStore: sessionStore,
		},
		babyConnections: make(map[string]*client.WebsocketConnection),
		babyManagers:     make(map[string]*client.WebsocketConnectionManager),
		pendingRetries:   make(map[string]*commandRetry),
	}

	if opts.MQTT != nil {
		instance.MQTTConnection = mqtt.NewConnection(*opts.MQTT)
	}

	return instance
}

// registerBabyConnection - registers a websocket connection for a specific baby
func (app *App) registerBabyConnection(babyUID string, conn *client.WebsocketConnection) {
	app.connectionsMutex.Lock()
	defer app.connectionsMutex.Unlock()
	app.babyConnections[babyUID] = conn
	log.Debug().Str("baby_uid", babyUID).Msg("Registered baby connection")
}

// unregisterBabyConnection - removes a websocket connection for a specific baby
func (app *App) unregisterBabyConnection(babyUID string) {
	app.connectionsMutex.Lock()
	defer app.connectionsMutex.Unlock()
	delete(app.babyConnections, babyUID)
	log.Debug().Str("baby_uid", babyUID).Msg("Unregistered baby connection")
}

// getBabyConnection - retrieves the websocket connection for a specific baby
func (app *App) getBabyConnection(babyUID string) *client.WebsocketConnection {
	app.connectionsMutex.RLock()
	defer app.connectionsMutex.RUnlock()
	return app.babyConnections[babyUID]
}

// registerBabyManager - registers a websocket manager for a specific baby
func (app *App) registerBabyManager(babyUID string, manager *client.WebsocketConnectionManager) {
	app.managersMutex.Lock()
	defer app.managersMutex.Unlock()
	app.babyManagers[babyUID] = manager
	log.Debug().Str("baby_uid", babyUID).Msg("Registered baby WebSocket manager")
}

// unregisterBabyManager - removes a websocket manager for a specific baby
func (app *App) unregisterBabyManager(babyUID string) {
	app.managersMutex.Lock()
	defer app.managersMutex.Unlock()
	delete(app.babyManagers, babyUID)
	log.Debug().Str("baby_uid", babyUID).Msg("Unregistered baby WebSocket manager")
}

// getBabyManager - retrieves the websocket manager for a specific baby
func (app *App) getBabyManager(babyUID string) *client.WebsocketConnectionManager {
	app.managersMutex.RLock()
	defer app.managersMutex.RUnlock()
	return app.babyManagers[babyUID]
}

// shouldAttemptReset - checks if reset behavior is enabled
func (app *App) shouldAttemptReset(babyUID string) bool {
	if !app.Opts.WebSocketReset.Enabled {
		return false
	}
	
	log.Info().
		Str("baby_uid", babyUID).
		Msg("Reset is enabled - allowing reset")
	return true
}

// addPendingRetry - adds a command to be retried after reconnection
func (app *App) addPendingRetry(babyUID string, commandType string, enabled bool) {
	app.retriesMutex.Lock()
	defer app.retriesMutex.Unlock()
	
	retryKey := fmt.Sprintf("%s_%s", babyUID, commandType)
	app.pendingRetries[retryKey] = &commandRetry{
		CommandType: commandType,
		BabyUID:     babyUID,
		Enabled:     enabled,
		Timestamp:   time.Now(),
	}
	
	log.Debug().
		Str("baby_uid", babyUID).
		Str("command_type", commandType).
		Bool("enabled", enabled).
		Msg("Added pending retry command")
}

// processPendingRetries - processes any pending retry commands for a baby
func (app *App) processPendingRetries(babyUID string) {
	app.retriesMutex.Lock()
	
	var retries []*commandRetry
	keysToDelete := make([]string, 0)
	
	for key, retry := range app.pendingRetries {
		if retry.BabyUID == babyUID {
			retries = append(retries, retry)
			keysToDelete = append(keysToDelete, key)
		}
	}
	
	// Remove processed retries
	for _, key := range keysToDelete {
		delete(app.pendingRetries, key)
	}
	
	app.retriesMutex.Unlock()
	
	// Execute retries
	for _, retry := range retries {
		conn := app.getBabyConnection(babyUID)
		if conn != nil {
			log.Info().
				Str("baby_uid", babyUID).
				Str("command_type", retry.CommandType).
				Bool("enabled", retry.Enabled).
				Msg("Retrying command after reconnection")
			
			switch retry.CommandType {
			case "light":
				// Send command directly without retry logic to avoid infinite loops
				sendLightCommand(retry.Enabled, conn)
			case "standby":
				// Send command directly without retry logic to avoid infinite loops
				sendStandbyCommand(retry.Enabled, conn)
			}
		}
	}
}

// Run - application main loop
func (app *App) Run(ctx utils.GracefulContext) {
	// Reauthorize if we don't have a token or we assume it is invalid
	app.RestClient.MaybeAuthorize(false)

	// Fetches babies info if they are not present in session
	app.RestClient.EnsureBabies()

	// RTMP
	if app.Opts.RTMP != nil {
		go rtmpserver.StartRTMPServer(app.Opts.RTMP.ListenAddr, app.BabyStateManager)
	}

	// MQTT - Register global command handlers
	if app.MQTTConnection != nil {
		// Register handlers that route commands to the correct baby
		app.MQTTConnection.RegisterLightHandler(func(babyUID string, enabled bool) {
			conn := app.getBabyConnection(babyUID)
			if conn != nil {
				log.Debug().Str("baby_uid", babyUID).Bool("enabled", enabled).Msg("Routing light command to baby")
				success := app.sendLightCommandWithReset(babyUID, enabled, conn)
				if !success {
					log.Warn().Str("baby_uid", babyUID).Bool("enabled", enabled).Msg("Light command failed after reset attempt")
				}
			} else {
				log.Warn().Str("baby_uid", babyUID).Msg("No connection found for baby, cannot send light command")
			}
		})
		
		app.MQTTConnection.RegisterStandyHandler(func(babyUID string, enabled bool) {
			conn := app.getBabyConnection(babyUID)
			if conn != nil {
				log.Debug().Str("baby_uid", babyUID).Bool("enabled", enabled).Msg("Routing standby command to baby")
				success := app.sendStandbyCommandWithReset(babyUID, enabled, conn)
				if !success {
					log.Warn().Str("baby_uid", babyUID).Bool("enabled", enabled).Msg("Standby command failed after reset attempt")
				}
			} else {
				log.Warn().Str("baby_uid", babyUID).Msg("No connection found for baby, cannot send standby command")
			}
		})
		
		ctx.RunAsChild(func(childCtx utils.GracefulContext) {
			app.MQTTConnection.Run(app.BabyStateManager, childCtx)
		})
	}

	// Start reading the data from the stream
	for _, babyInfo := range app.SessionStore.Session.Babies {
		_babyInfo := babyInfo
		ctx.RunAsChild(func(childCtx utils.GracefulContext) {
			app.handleBaby(_babyInfo, childCtx)
		})
	}

	// Start serving content over HTTP
	if app.Opts.HTTPEnabled {
		go serve(app.SessionStore.Session.Babies, app.Opts.DataDirectories)
	}

	<-ctx.Done()
}

func (app *App) handleBaby(baby baby.Baby, ctx utils.GracefulContext) {
	if app.Opts.RTMP != nil || app.MQTTConnection != nil {
		// Websocket connection
		ws := client.NewWebsocketConnectionManager(baby.UID, baby.CameraUID, app.SessionStore.Session, app.RestClient, app.BabyStateManager)
		
		// Register the WebSocket manager for forced reconnection
		app.registerBabyManager(baby.UID, ws)

		ws.WithReadyConnection(func(conn *client.WebsocketConnection, childCtx utils.GracefulContext) {
			app.runWebsocket(baby.UID, conn, childCtx)
		})

		if app.Opts.EventPolling.Enabled {
			go app.pollMessages(baby.UID, app.BabyStateManager)
		}

		ctx.RunAsChild(func(childCtx utils.GracefulContext) {
			ws.RunWithinContext(childCtx)
		})
	}

	// Wait for context to complete
	<-ctx.Done()
	
	// Clean up when context is done
	app.unregisterBabyManager(baby.UID)
}

func (app *App) pollMessages(babyUID string, babyStateManager *baby.StateManager) {
	newMessages := app.RestClient.FetchNewMessages(babyUID, app.Opts.EventPolling.MessageTimeout)

	for _, msg := range newMessages {
		switch msg.Type {
		case message.SoundEventMessageType:
			go babyStateManager.NotifySoundSubscribers(babyUID, time.Time(msg.Time))
			break
		case message.MotionEventMessageType:
			go babyStateManager.NotifyMotionSubscribers(babyUID, time.Time(msg.Time))
			break
		}
	}

	// wait for the specified interval
	time.Sleep(app.Opts.EventPolling.PollingInterval)
	app.pollMessages(babyUID, babyStateManager)
}

func (app *App) runWebsocket(babyUID string, conn *client.WebsocketConnection, childCtx utils.GracefulContext) {
	// Register this connection in the connection registry
	app.registerBabyConnection(babyUID, conn)
	
	// Process any pending retry commands after reconnection
	app.processPendingRetries(babyUID)
	
	// Reading sensor data
	conn.RegisterMessageHandler(func(m *client.Message, conn *client.WebsocketConnection) {
		// Sensor request initiated by us on start (or some other client, we don't care)
		if *m.Type == client.Message_RESPONSE && m.Response != nil {
			if *m.Response.RequestType == client.RequestType_GET_SENSOR_DATA && len(m.Response.SensorData) > 0 {
				processSensorData(babyUID, m.Response.SensorData, app.BabyStateManager)
			} else if *m.Response.RequestType == client.RequestType_GET_CONTROL && m.Response.Control != nil {
				processLight(babyUID, m.Response.Control, app.BabyStateManager)
			} else if *m.Response.RequestType == client.RequestType_GET_SETTINGS && m.Response.Settings != nil {
				processStandby(babyUID, m.Response.Settings, app.BabyStateManager)
			}
		} else

		// Communication initiated from a cam
		// Note: it sends the updates periodically on its own + whenever some significant change occurs
		if *m.Type == client.Message_REQUEST && m.Request != nil {
			if *m.Request.Type == client.RequestType_PUT_SENSOR_DATA && len(m.Request.SensorData_) > 0 {
				processSensorData(babyUID, m.Request.SensorData_, app.BabyStateManager)
			} else if *m.Request.Type == client.RequestType_PUT_CONTROL && m.Request.Control != nil {
				processLight(babyUID, m.Request.Control, app.BabyStateManager)
			} else if *m.Request.Type == client.RequestType_PUT_SETTINGS && m.Request.Settings != nil {
				processStandby(babyUID, m.Request.Settings, app.BabyStateManager)
			}
		}
	})


	// Get the initial state of the light
	conn.SendRequest(client.RequestType_GET_CONTROL, &client.Request{GetControl_: &client.GetControl{
		NightLight: utils.ConstRefBool(true),
	}})

	// Ask for sensor data (initial request)
	conn.SendRequest(client.RequestType_GET_SENSOR_DATA, &client.Request{
		GetSensorData: &client.GetSensorData{
			All: utils.ConstRefBool(true),
		},
	})

	// Ask for status
	// conn.SendRequest(client.RequestType_GET_STATUS, &client.Request{
	// 	GetStatus_: &client.GetStatus{
	// 		All: utils.ConstRefBool(true),
	// 	},
	// })

	// Ask for logs
	// conn.SendRequest(client.RequestType_GET_LOGS, &client.Request{
	// 	GetLogs: &client.GetLogs{
	// 		Url: utils.ConstRefStr("http://192.168.3.234:8080/log"),
	// 	},
	// })

	var cleanup func()

	// Local streaming
	if app.Opts.RTMP != nil {
		initializeLocalStreaming := func() {
			requestLocalStreaming(babyUID, app.getLocalStreamURL(babyUID), client.Streaming_STARTED, conn, app.BabyStateManager)
		}

		// Watch for stream liveness change
		unsubscribe := app.BabyStateManager.Subscribe(func(updatedBabyUID string, stateUpdate baby.State) {
			// Do another streaming request if stream just turned unhealthy
			if updatedBabyUID == babyUID && stateUpdate.StreamState != nil && *stateUpdate.StreamState == baby.StreamState_Unhealthy {
				// Prevent duplicate request if we already received failure
				if app.BabyStateManager.GetBabyState(babyUID).GetStreamRequestState() != baby.StreamRequestState_RequestFailed {
					go initializeLocalStreaming()
				}
			}
		})

		cleanup = func() {
			// Stop listening for stream liveness change
			unsubscribe()

			// Stop local streaming
			state := app.BabyStateManager.GetBabyState(babyUID)
			if state.GetIsWebsocketAlive() && state.GetStreamState() == baby.StreamState_Alive {
				requestLocalStreaming(babyUID, app.getLocalStreamURL(babyUID), client.Streaming_STOPPED, conn, app.BabyStateManager)
			}
		}

		// Initialize local streaming upon connection if we know that the stream is not alive
		babyState := app.BabyStateManager.GetBabyState(babyUID)
		if babyState.GetStreamState() != baby.StreamState_Alive {
			if babyState.GetStreamRequestState() != baby.StreamRequestState_Requested || babyState.GetStreamState() == baby.StreamState_Unhealthy {
				go initializeLocalStreaming()
			}
		}
	}

	<-childCtx.Done()
	
	// Unregister this connection from the connection registry
	app.unregisterBabyConnection(babyUID)
	
	if cleanup != nil {
		cleanup()
	}
}

func (app *App) getRemoteStreamURL(babyUID string) string {
	return fmt.Sprintf("rtmps://media-secured.nanit.com/nanit/%v.%v", babyUID, app.SessionStore.Session.AuthToken)
}

func (app *App) getLocalStreamURL(babyUID string) string {
	if app.Opts.RTMP != nil {
		tpl := "rtmp://{publicAddr}/local/{babyUid}"
		return strings.NewReplacer("{publicAddr}", app.Opts.RTMP.PublicAddr, "{babyUid}", babyUID).Replace(tpl)
	}

	return ""
}
