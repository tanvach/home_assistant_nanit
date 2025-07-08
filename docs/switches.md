# Switches

App exposes cam controls by accepting MQTT commands and publishing state updates. See `NANIT_MQTT_*` variables in the [.env.sample](../.env.sample) file for configuration.

## Reliability Configuration

For improved reliability when camera WebSocket connections become unresponsive, the app supports automatic connection reset and command retry:

### Environment Variables

- **`NANIT_MQTT_RESET_WHEN_FAILED`** (default: `false`)
  - Enables automatic WebSocket reset when MQTT commands fail
  - When `true`, failed commands trigger WebSocket reconnection and automatic retry
  - Helps resolve "half-dead" connections that receive data but can't send commands
  - Recommended setting: `true` for production environments

- **`NANIT_WEBSOCKET_TIMEOUT`** (default: `1s`)
  - Timeout for camera command responses before marking as failed
  - Only used when `NANIT_MQTT_RESET_WHEN_FAILED=true`
  - Lower values provide faster failure detection but may cause false positives
  - Higher values are more tolerant but slower to detect real failures
  - Recommended range: `500ms` to `2s`

### Example Configuration

```bash
# Enable automatic WebSocket reset on command failures
NANIT_MQTT_RESET_WHEN_FAILED=true

# Set command timeout to 1 second
NANIT_WEBSOCKET_TIMEOUT=1s
```

**Note**: When disabled (default), the app behaves exactly as before with no additional overhead.

## Command Topics

The app subscribes to the following command topics to control camera functions:

- `nanit/babies/{baby_uid}/night_light/switch` - turn night light on/off (payload: `true` or `false`)
- `nanit/babies/{baby_uid}/standby/switch` - enable/disable standby mode (payload: `true` or `false`)

## State Topics

The app publishes the current state to these topics:

- `nanit/babies/{baby_uid}/night_light` - current night light state (bool)
- `nanit/babies/{baby_uid}/standby` - current standby state (bool)

## Example Usage

Turn on night light:
```bash
mosquitto_pub -h your-broker -t "nanit/babies/your-baby-uid/night_light/switch" -m "true"
```

Turn off night light:
```bash
mosquitto_pub -h your-broker -t "nanit/babies/your-baby-uid/night_light/switch" -m "false"
```

## Home Assistant Integration

You can configure these switches in your [Home Assistant setup](./home-assistant.md) using MQTT switches:

```yaml
mqtt:
  switch:
    - name: "Baby Monitor Night Light"
      state_topic: "nanit/babies/your-baby-uid/night_light"
      command_topic: "nanit/babies/your-baby-uid/night_light/switch"
      payload_on: "true"
      payload_off: "false"
      state_on: "true"
      state_off: "false"
      
    - name: "Baby Monitor Standby"
      state_topic: "nanit/babies/your-baby-uid/standby"
      command_topic: "nanit/babies/your-baby-uid/standby/switch"
      payload_on: "true"
      payload_off: "false"
      state_on: "true"
      state_off: "false"
```

## Troubleshooting

### Common Issues

**MQTT switches stop responding after hours of operation:**
- Enable automatic WebSocket reset by setting `NANIT_MQTT_RESET_WHEN_FAILED=true`
- This resolves "half-dead" WebSocket connections that can receive data but not send commands
- Look for log messages like "Light command failed, attempting WebSocket reset" and "Retrying command after reconnection"

**Commands timeout frequently:**
- Adjust `NANIT_WEBSOCKET_TIMEOUT` (try increasing to `1s` or `2s`)
- Check your network connection between the app and Nanit servers
- Verify camera isn't overwhelmed by multiple stream connections

### Debugging

In case you run into trouble and need to see what is going on, you can try using [MQTT Explorer](http://mqtt-explorer.com/) to monitor both the command and state topics.

With debug logging enabled, you'll see detailed WebSocket communication:
```
DBG Sending message data="type:REQUEST request:{id:1 type:PUT_CONTROL control:{nightLight:LIGHT_ON}}"
DBG Received message data="type:RESPONSE response:{requestId:1 requestType:PUT_CONTROL statusCode:200}"
```

**Note**: Make sure to replace `your-baby-uid` with your actual baby UID, which you can find in the application logs or MQTT state topics. 