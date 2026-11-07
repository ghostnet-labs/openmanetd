// Package weather polls a weather sensor wired to this node and shares
// its readings with every node on the mesh.
//
// A node with a sensor (normally a mast) reads it every
// weather.pollInterval over RS485 Modbus-RTU, keeps a short history for
// the gust (highest wind in weather.gustWindow), the 3-hour pressure
// change and the last hour of rain, and publishes a WeatherReport over
// alfred (DATA_TYPE_WEATHER) every weather.publishInterval. Every node,
// with or without a sensor, fetches the reports other nodes publish so
// the web app and ATAK can show every station on the mesh.
//
// The only supported hardware is the DFRobot SEN0657 ultrasonic 7-in-1
// sensor. A simulated sensor (weather.sensor.model sim) produces
// plausible readings for testing without hardware.
package weather
