// SPDX-License-Identifier: Apache-2.0

package api

import "github.com/polipan/polipan/server/internal/tpl"

// TrialPlanForTest expands a manifest with sample inputs and checks every service config. Only for tests.
func TrialPlanForTest(m *tpl.Manifest) error {
	h := &Handlers{}
	_, err := h.trialPlan(m)
	return err
}
