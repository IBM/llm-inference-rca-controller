/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package types

// TriggerType indicates which scaling trigger should be used
type TriggerType string

const (
	// TriggerTypeScaleUp indicates scale-up should be triggered (high latency violation)
	TriggerTypeScaleUp TriggerType = "scaleup"
	// TriggerTypeScaleDown indicates scale-down should be triggered (overprovisioning)
	TriggerTypeScaleDown TriggerType = "scaledown"
)
