// Copyright (c) 2026 PGSTY
//
// This file is part of the Silo object storage client.
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published by
// the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.

package cmd

import (
	stdjson "encoding/json"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/prometheus/prom2json"
)

func TestParsePrometheusResults(t *testing.T) {
	input := strings.NewReader(`# HELP z_metric Last metric alphabetically.
# TYPE z_metric gauge
z_metric 2
# HELP "a.metric" UTF-8 metric name.
# TYPE "a.metric" gauge
{"a.metric"} 3
# HELP a_metric First metric alphabetically.
# TYPE a_metric counter
a_metric 1
`)

	results, err := parsePrometheusResults(input)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 metric families, got %d", len(results))
	}
	if results[0].Name != "a.metric" || results[1].Name != "a_metric" || results[2].Name != "z_metric" {
		t.Fatalf("metric families are not sorted: %q, %q, %q", results[0].Name, results[1].Name, results[2].Name)
	}

	rendered := prometheusMetricsReader{Reader: strings.NewReader(`# TYPE sample gauge
sample 1
`)}.JSON()
	var decoded []*prom2json.Family
	if err = stdjson.Unmarshal([]byte(rendered), &decoded); err != nil {
		t.Fatalf("JSON output is invalid: %v", err)
	}
	if len(decoded) != 1 || decoded[0].Name != "sample" {
		t.Fatalf("unexpected JSON output: %s", rendered)
	}
}

func TestParsePrometheusResultsRejectsInvalidText(t *testing.T) {
	if _, err := parsePrometheusResults(strings.NewReader("metric not-a-number\n")); err == nil {
		t.Fatal("expected invalid Prometheus text to return an error")
	}
}

func TestPrometheusMetricsReaderStringPreservesText(t *testing.T) {
	const input = "# TYPE sample gauge\nsample 1\n"

	oldStdout := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		os.Stdout = oldStdout
		reader.Close()
	}()
	os.Stdout = writer

	if result := (prometheusMetricsReader{Reader: strings.NewReader(input)}).String(); result != "" {
		t.Fatalf("expected empty String result, got %q", result)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}

	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(output) != input {
		t.Fatalf("plain-text metrics changed: got %q, want %q", output, input)
	}
}
