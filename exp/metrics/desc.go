// Copyright 2026 The Prometheus Authors
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package metrics

import (
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/model"
)

// reservedLabelPrefix is not legal in user-supplied label names.
const reservedLabelPrefix = "__"

// Desc describes a metric and holds its precomputed, immutable labels.
//
// Instances are created internally from [CounterOpts] and are safe to read
// concurrently.
type Desc struct {
	// fqName is the fully-qualified metric name.
	fqName string

	// help describes the metric.
	help string

	// constLabelPairs contains precalculated DTO label pairs sorted by label
	// name.
	constLabelPairs []*dto.LabelPair

	// err records an invalid option and is surfaced when the metric is used.
	err error
}

// String returns the fully-qualified name of the described metric.
func (d *Desc) String() string {
	return d.fqName
}

// newDesc validates the options and builds a descriptor with precomputed
// constant label pairs. It panics on invalid names, labels, or label values.
func newDesc(opts CounterOpts) *Desc {
	fqName := BuildFQName(opts.Namespace, opts.Subsystem, opts.Name)
	desc := &Desc{fqName: fqName, help: opts.Help}

	if !model.UTF8Validation.IsValidMetricName(fqName) {
		desc.err = fmt.Errorf("%q is not a valid metric name", fqName)
	}

	labelNames := make([]string, 0, len(opts.ConstLabels))
	for labelName := range opts.ConstLabels {
		if !model.UTF8Validation.IsValidLabelName(labelName) ||
			strings.HasPrefix(labelName, reservedLabelPrefix) {
			desc.err = errorsJoin(desc.err,
				fmt.Errorf("%q is not a valid label name for metric %q", labelName, fqName))
		}
		labelNames = append(labelNames, labelName)
	}
	sort.Strings(labelNames)

	desc.constLabelPairs = make([]*dto.LabelPair, 0, len(labelNames))
	for _, labelName := range labelNames {
		labelValue := opts.ConstLabels[labelName]
		if !utf8.ValidString(labelValue) {
			desc.err = errorsJoin(desc.err,
				fmt.Errorf("label %s: value %q is not valid UTF-8", labelName, labelValue))
		}
		desc.constLabelPairs = append(desc.constLabelPairs, &dto.LabelPair{
			Name:  stringPtr(labelName),
			Value: stringPtr(labelValue),
		})
	}

	if desc.err != nil {
		panic(desc.err)
	}
	return desc
}

// BuildFQName joins the name components with underscores and ignores empty
// components.
func BuildFQName(namespace, subsystem, name string) string {
	if name == "" {
		return ""
	}
	var builder strings.Builder
	if namespace != "" {
		builder.WriteString(namespace)
		builder.WriteByte('_')
	}
	if subsystem != "" {
		builder.WriteString(subsystem)
		builder.WriteByte('_')
	}
	builder.WriteString(name)
	return builder.String()
}

// errorsJoin combines validation errors without importing the errors package
// only for this helper.
func errorsJoin(current, additional error) error {
	if current == nil {
		return additional
	}
	return fmt.Errorf("%w; %w", current, additional)
}

func stringPtr(value string) *string {
	return &value
}
