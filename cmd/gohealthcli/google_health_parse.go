package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type googleHealthDataPointList struct {
	dataPoints    []json.RawMessage
	nextPageToken string
}

type archivedDataPoint struct {
	providerName         string
	connectionID         string
	dataType             string
	upstreamResourceName string
	recordKind           string
	startTimeUTC         string
	endTimeUTC           string
	startCivilTime       string
	endCivilTime         string
	providerCivilDate    string
	timezoneMetadataJSON string
	dataSourceJSON       string
	sourceFamilyFilter   string
	rawJSON              string
}

type googleHealthDataPointEnvelope struct {
	name           string
	dataPointName  string
	dataSourceJSON string
	fields         map[string]json.RawMessage
}

type googleHealthIntervalFields struct {
	StartTime      string          `json:"startTime"`
	StartUTCOffset string          `json:"startUtcOffset"`
	EndTime        string          `json:"endTime"`
	EndUTCOffset   string          `json:"endUtcOffset"`
	CivilStartTime json.RawMessage `json:"civilStartTime"`
	CivilEndTime   json.RawMessage `json:"civilEndTime"`
}

type parsedGoogleHealthInterval struct {
	startTimeUTC         string
	endTimeUTC           string
	startCivilTime       string
	endCivilTime         string
	providerCivilDate    string
	timezoneMetadataJSON string
}

type archivedRollup struct {
	providerName         string
	connectionID         string
	dataType             string
	rollupKind           string
	windowStartUTC       string
	windowEndUTC         string
	civilDate            string
	timezoneMetadataJSON string
	rawJSON              string
}

func parseGoogleHealthDataPointList(body []byte) (googleHealthDataPointList, error) {
	var raw struct {
		DataPoints    []json.RawMessage `json:"dataPoints"`
		NextPageToken string            `json:"nextPageToken"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return googleHealthDataPointList{}, errors.New("Google Health Data Point list response is not valid JSON")
	}
	return googleHealthDataPointList{dataPoints: raw.DataPoints, nextPageToken: raw.NextPageToken}, nil
}

func parseGoogleHealthDataPoint(connection archivedConnection, dataType string, rawPoint json.RawMessage, sourceFamilyFilter string) (archivedDataPoint, error) {
	if jsonField, recordKind, ok := googleHealthIntervalShapedDataPointShape(dataType); ok {
		return parseGoogleHealthIntervalShapedDataPoint(connection, dataType, rawPoint, sourceFamilyFilter, jsonField, recordKind)
	}
	if googleHealthSampleDataPointJSONField(dataType) != "" {
		return parseGoogleHealthSampleDataPoint(connection, dataType, rawPoint, sourceFamilyFilter)
	}
	if googleHealthDailyDataPointJSONField(dataType) != "" {
		return parseGoogleHealthDailyDataPoint(connection, dataType, rawPoint, sourceFamilyFilter)
	}
	return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point is not supported", dataType)
}

func parseGoogleHealthDataPointEnvelope(dataType string, rawPoint json.RawMessage) (googleHealthDataPointEnvelope, error) {
	var raw struct {
		Name          string                     `json:"name"`
		DataPointName string                     `json:"dataPointName"`
		DataSource    json.RawMessage            `json:"dataSource"`
		Fields        map[string]json.RawMessage `json:"-"`
	}
	if err := json.Unmarshal(rawPoint, &raw.Fields); err != nil {
		return googleHealthDataPointEnvelope{}, fmt.Errorf("Google Health %s Data Point is not valid JSON", dataType)
	}
	if err := json.Unmarshal(rawPoint, &raw); err != nil {
		return googleHealthDataPointEnvelope{}, fmt.Errorf("Google Health %s Data Point is not valid JSON", dataType)
	}
	dataSourceJSON := "{}"
	if len(raw.DataSource) != 0 && string(raw.DataSource) != "null" {
		var err error
		dataSourceJSON, err = compactJSONString(raw.DataSource)
		if err != nil {
			return googleHealthDataPointEnvelope{}, fmt.Errorf("Google Health %s Data Point dataSource is not valid JSON", dataType)
		}
	}
	return googleHealthDataPointEnvelope{
		name:           raw.Name,
		dataPointName:  raw.DataPointName,
		dataSourceJSON: dataSourceJSON,
		fields:         raw.Fields,
	}, nil
}

func (envelope googleHealthDataPointEnvelope) upstreamResourceName() string {
	if envelope.name != "" {
		return envelope.name
	}
	return envelope.dataPointName
}

// requiredField returns the named value object, or the missing-value
// error every Data Point parser shape reports for an absent field.
func (envelope googleHealthDataPointEnvelope) requiredField(dataType, jsonField string) (json.RawMessage, error) {
	rawValue, ok := envelope.fields[jsonField]
	if !ok || len(rawValue) == 0 || string(rawValue) == "null" {
		return nil, fmt.Errorf("Google Health %s Data Point missing %s value", dataType, jsonField)
	}
	return rawValue, nil
}

// parseGoogleHealthDataPointHead performs the envelope decode shared
// by every Data Point parser shape: the canonical raw JSON archived on
// the row plus the name / dataSource / field-map envelope.
func parseGoogleHealthDataPointHead(dataType string, rawPoint json.RawMessage) (string, googleHealthDataPointEnvelope, error) {
	canonicalRaw, err := compactJSONString(rawPoint)
	if err != nil {
		return "", googleHealthDataPointEnvelope{}, fmt.Errorf("Google Health %s Data Point is not valid JSON", dataType)
	}
	envelope, err := parseGoogleHealthDataPointEnvelope(dataType, rawPoint)
	if err != nil {
		return "", googleHealthDataPointEnvelope{}, err
	}
	return canonicalRaw, envelope, nil
}

func parseGoogleHealthIntervalMetadata(dataType string, interval googleHealthIntervalFields) (parsedGoogleHealthInterval, error) {
	if interval.StartTime == "" || interval.EndTime == "" {
		return parsedGoogleHealthInterval{}, fmt.Errorf("Google Health %s Data Point missing interval startTime or endTime", dataType)
	}
	startTimeUTC, err := normalizeGoogleTimestamp(interval.StartTime)
	if err != nil {
		return parsedGoogleHealthInterval{}, fmt.Errorf("Google Health %s Data Point startTime: %w", dataType, err)
	}
	endTimeUTC, err := normalizeGoogleTimestamp(interval.EndTime)
	if err != nil {
		return parsedGoogleHealthInterval{}, fmt.Errorf("Google Health %s Data Point endTime: %w", dataType, err)
	}
	startCivilTime, providerCivilDate, err := googleCivilDateTimeText(interval.CivilStartTime)
	if err != nil {
		return parsedGoogleHealthInterval{}, fmt.Errorf("Google Health %s Data Point civilStartTime: %w", dataType, err)
	}
	endCivilTime, _, err := googleCivilDateTimeText(interval.CivilEndTime)
	if err != nil {
		return parsedGoogleHealthInterval{}, fmt.Errorf("Google Health %s Data Point civilEndTime: %w", dataType, err)
	}
	timezoneMetadata, err := googleIntervalTimezoneMetadataJSON(interval.StartUTCOffset, interval.EndUTCOffset)
	if err != nil {
		return parsedGoogleHealthInterval{}, err
	}
	return parsedGoogleHealthInterval{
		startTimeUTC:         startTimeUTC,
		endTimeUTC:           endTimeUTC,
		startCivilTime:       startCivilTime,
		endCivilTime:         endCivilTime,
		providerCivilDate:    providerCivilDate,
		timezoneMetadataJSON: timezoneMetadata,
	}, nil
}

// parseGoogleHealthIntervalShapedDataPoint is the single parser for
// the interval-shaped Data Point kinds (steps, interval, session).
// The Data Type catalog supplies the two values the kinds differ in:
// the JSON field holding the upstream value object and the record
// kind stored on the archived row (#278).
func parseGoogleHealthIntervalShapedDataPoint(connection archivedConnection, dataType string, rawPoint json.RawMessage, sourceFamilyFilter, jsonField, recordKind string) (archivedDataPoint, error) {
	canonicalRaw, envelope, err := parseGoogleHealthDataPointHead(dataType, rawPoint)
	if err != nil {
		return archivedDataPoint{}, err
	}
	rawValue, err := envelope.requiredField(dataType, jsonField)
	if err != nil {
		return archivedDataPoint{}, err
	}
	var value struct {
		Interval googleHealthIntervalFields `json:"interval"`
	}
	if err := json.Unmarshal(rawValue, &value); err != nil {
		return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point %s is not valid JSON", dataType, jsonField)
	}
	interval, err := parseGoogleHealthIntervalMetadata(dataType, value.Interval)
	if err != nil {
		return archivedDataPoint{}, err
	}
	return archivedDataPoint{
		providerName:         connection.providerName,
		connectionID:         connection.id,
		dataType:             dataType,
		upstreamResourceName: envelope.upstreamResourceName(),
		recordKind:           recordKind,
		startTimeUTC:         interval.startTimeUTC,
		endTimeUTC:           interval.endTimeUTC,
		startCivilTime:       interval.startCivilTime,
		endCivilTime:         interval.endCivilTime,
		providerCivilDate:    interval.providerCivilDate,
		timezoneMetadataJSON: interval.timezoneMetadataJSON,
		dataSourceJSON:       envelope.dataSourceJSON,
		sourceFamilyFilter:   sourceFamilyFilter,
		rawJSON:              canonicalRaw,
	}, nil
}

func parseGoogleHealthSampleDataPoint(connection archivedConnection, dataType string, rawPoint json.RawMessage, sourceFamilyFilter string) (archivedDataPoint, error) {
	canonicalRaw, envelope, err := parseGoogleHealthDataPointHead(dataType, rawPoint)
	if err != nil {
		return archivedDataPoint{}, err
	}
	jsonField := googleHealthSampleDataPointJSONField(dataType)
	rawSample, err := envelope.requiredField(dataType, jsonField)
	if err != nil {
		return archivedDataPoint{}, err
	}
	var sample struct {
		SampleTime struct {
			PhysicalTime string          `json:"physicalTime"`
			UTCOffset    string          `json:"utcOffset"`
			CivilTime    json.RawMessage `json:"civilTime"`
		} `json:"sampleTime"`
	}
	if err := json.Unmarshal(rawSample, &sample); err != nil {
		return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point %s is not valid JSON", dataType, jsonField)
	}
	if sample.SampleTime.PhysicalTime == "" {
		return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point missing sampleTime physicalTime", dataType)
	}
	startTimeUTC, err := normalizeGoogleTimestamp(sample.SampleTime.PhysicalTime)
	if err != nil {
		return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point sampleTime physicalTime: %w", dataType, err)
	}
	startCivilTime, providerCivilDate, err := googleCivilDateTimeText(sample.SampleTime.CivilTime)
	if err != nil {
		return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point sampleTime civilTime: %w", dataType, err)
	}
	timezoneMetadata, err := googleSampleTimezoneMetadataJSON(sample.SampleTime.UTCOffset)
	if err != nil {
		return archivedDataPoint{}, err
	}
	return archivedDataPoint{
		providerName:         connection.providerName,
		connectionID:         connection.id,
		dataType:             dataType,
		upstreamResourceName: envelope.upstreamResourceName(),
		recordKind:           "sample",
		startTimeUTC:         startTimeUTC,
		startCivilTime:       startCivilTime,
		providerCivilDate:    providerCivilDate,
		timezoneMetadataJSON: timezoneMetadata,
		dataSourceJSON:       envelope.dataSourceJSON,
		sourceFamilyFilter:   sourceFamilyFilter,
		rawJSON:              canonicalRaw,
	}, nil
}

func parseGoogleHealthDailyDataPoint(connection archivedConnection, dataType string, rawPoint json.RawMessage, sourceFamilyFilter string) (archivedDataPoint, error) {
	canonicalRaw, envelope, err := parseGoogleHealthDataPointHead(dataType, rawPoint)
	if err != nil {
		return archivedDataPoint{}, err
	}
	shape, ok := googleHealthDailyDataPointShapeForDataType(dataType)
	if !ok {
		return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point is not supported", dataType)
	}
	rawDaily, err := envelope.requiredField(dataType, shape.jsonField)
	if err != nil {
		return archivedDataPoint{}, err
	}
	var daily struct {
		Date json.RawMessage `json:"date"`
	}
	if err := json.Unmarshal(rawDaily, &daily); err != nil {
		return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point %s is not valid JSON", dataType, shape.jsonField)
	}
	providerCivilDate, err := googleDateText(daily.Date)
	if err != nil {
		return archivedDataPoint{}, fmt.Errorf("Google Health %s Data Point date: %w", dataType, err)
	}
	return archivedDataPoint{
		providerName:         connection.providerName,
		connectionID:         connection.id,
		dataType:             dataType,
		upstreamResourceName: envelope.upstreamResourceName(),
		recordKind:           "daily",
		providerCivilDate:    providerCivilDate,
		dataSourceJSON:       envelope.dataSourceJSON,
		sourceFamilyFilter:   sourceFamilyFilter,
		rawJSON:              canonicalRaw,
	}, nil
}

func compactJSONString(raw json.RawMessage) (string, error) {
	var out bytes.Buffer
	if err := json.Compact(&out, raw); err != nil {
		return "", err
	}
	return out.String(), nil
}

func normalizeGoogleTimestamp(value string) (string, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return "", errors.New("expected RFC3339 timestamp")
	}
	return parsed.UTC().Format(time.RFC3339Nano), nil
}

func googleCivilDateTimeText(raw json.RawMessage) (string, string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", "", nil
	}
	var value struct {
		Date struct {
			Year  int `json:"year"`
			Month int `json:"month"`
			Day   int `json:"day"`
		} `json:"date"`
		Time *struct {
			Hours   int `json:"hours"`
			Minutes int `json:"minutes"`
			Seconds int `json:"seconds"`
			Nanos   int `json:"nanos"`
		} `json:"time"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", "", errors.New("not valid JSON")
	}
	if value.Date.Year == 0 || value.Date.Month == 0 || value.Date.Day == 0 {
		return "", "", errors.New("missing date")
	}
	date := fmt.Sprintf("%04d-%02d-%02d", value.Date.Year, value.Date.Month, value.Date.Day)
	if value.Time == nil {
		return date, date, nil
	}
	text := fmt.Sprintf("%sT%02d:%02d:%02d", date, value.Time.Hours, value.Time.Minutes, value.Time.Seconds)
	if value.Time.Nanos != 0 {
		fraction := fmt.Sprintf("%09d", value.Time.Nanos)
		fraction = strings.TrimRight(fraction, "0")
		text += "." + fraction
	}
	return text, date, nil
}

func googleDateText(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", errors.New("missing date")
	}
	var value struct {
		Year  int `json:"year"`
		Month int `json:"month"`
		Day   int `json:"day"`
	}
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", errors.New("not valid JSON")
	}
	if value.Year == 0 || value.Month == 0 || value.Day == 0 {
		return "", errors.New("missing date")
	}
	return fmt.Sprintf("%04d-%02d-%02d", value.Year, value.Month, value.Day), nil
}

func googleIntervalTimezoneMetadataJSON(startUTCOffset, endUTCOffset string) (string, error) {
	metadata := map[string]string{}
	if startUTCOffset != "" {
		metadata["start_utc_offset"] = startUTCOffset
	}
	if endUTCOffset != "" {
		metadata["end_utc_offset"] = endUTCOffset
	}
	if len(metadata) == 0 {
		return "", nil
	}
	content, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func googleSampleTimezoneMetadataJSON(utcOffset string) (string, error) {
	if utcOffset == "" {
		return "", nil
	}
	content, err := json.Marshal(map[string]string{"utc_offset": utcOffset})
	if err != nil {
		return "", err
	}
	return string(content), nil
}

// googleDailyRollupTimeMetadataJSON is Data Type-agnostic: callers
// wrap the returned error with the active Data Type so the message
// reflects the row that failed (steps vs floors vs …). Keeping the
// helper itself generic avoids stringly-typed plumbing of the Data
// Type through one more layer.
func googleDailyRollupTimeMetadataJSON(civilStartTime, civilEndTime json.RawMessage) (string, error) {
	metadata := map[string]json.RawMessage{}
	if len(civilStartTime) != 0 && string(civilStartTime) != "null" {
		start, err := compactJSONString(civilStartTime)
		if err != nil {
			return "", errors.New("daily Rollup civilStartTime is not valid JSON")
		}
		metadata["civil_start_time"] = json.RawMessage(start)
	}
	if len(civilEndTime) != 0 && string(civilEndTime) != "null" {
		end, err := compactJSONString(civilEndTime)
		if err != nil {
			return "", errors.New("daily Rollup civilEndTime is not valid JSON")
		}
		metadata["civil_end_time"] = json.RawMessage(end)
	}
	if len(metadata) == 0 {
		return "", nil
	}
	content, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	return string(content), nil
}
