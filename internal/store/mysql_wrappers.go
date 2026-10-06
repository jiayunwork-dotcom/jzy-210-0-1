package store

import "context"

// Single-operation wrappers satisfying the embedded Tx of Store.
func (m *MySQL) SaveMachine(ctx context.Context, rec *MachineRecord) error {
	return m.WithTx(ctx, func(tx Tx) error { return tx.SaveMachine(ctx, rec) })
}
func (m *MySQL) GetMachine(ctx context.Context, id string) (*MachineRecord, error) {
	var out *MachineRecord
	err := m.WithTx(ctx, func(tx Tx) error {
		var e error
		out, e = tx.GetMachine(ctx, id)
		return e
	})
	return out, err
}
func (m *MySQL) ListMachines(ctx context.Context) ([]*MachineRecord, error) {
	var out []*MachineRecord
	err := m.WithTx(ctx, func(tx Tx) error {
		var e error
		out, e = tx.ListMachines(ctx)
		return e
	})
	return out, err
}
func (m *MySQL) SaveJob(ctx context.Context, rec *JobRecord) error {
	return m.WithTx(ctx, func(tx Tx) error { return tx.SaveJob(ctx, rec) })
}
func (m *MySQL) GetJob(ctx context.Context, id string, forUpdate bool) (*JobRecord, error) {
	var out *JobRecord
	err := m.WithTx(ctx, func(tx Tx) error {
		var e error
		out, e = tx.GetJob(ctx, id, forUpdate)
		return e
	})
	return out, err
}
func (m *MySQL) ListJobsByMachine(ctx context.Context, machineID string) ([]*JobRecord, error) {
	var out []*JobRecord
	err := m.WithTx(ctx, func(tx Tx) error {
		var e error
		out, e = tx.ListJobsByMachine(ctx, machineID)
		return e
	})
	return out, err
}
func (m *MySQL) AppendEvent(ctx context.Context, e *EventRecord) (*EventRecord, error) {
	var out *EventRecord
	err := m.WithTx(ctx, func(tx Tx) error {
		var x error
		out, x = tx.AppendEvent(ctx, e)
		return x
	})
	return out, err
}
func (m *MySQL) ListEvents(ctx context.Context, jobID string) ([]EventRecord, error) {
	var out []EventRecord
	err := m.WithTx(ctx, func(tx Tx) error {
		var e error
		out, e = tx.ListEvents(ctx, jobID)
		return e
	})
	return out, err
}
func (m *MySQL) FindEventByClientID(ctx context.Context, jobID, clientID string) (*EventRecord, error) {
	var out *EventRecord
	err := m.WithTx(ctx, func(tx Tx) error {
		var e error
		out, e = tx.FindEventByClientID(ctx, jobID, clientID)
		return e
	})
	return out, err
}
func (m *MySQL) UpsertEstimate(ctx context.Context, r *EstimateRecord) error {
	return m.WithTx(ctx, func(tx Tx) error { return tx.UpsertEstimate(ctx, r) })
}
func (m *MySQL) ListEstimates(ctx context.Context, machineID string) ([]EstimateRecord, error) {
	var out []EstimateRecord
	err := m.WithTx(ctx, func(tx Tx) error {
		var e error
		out, e = tx.ListEstimates(ctx, machineID)
		return e
	})
	return out, err
}
func (m *MySQL) PutCurrentCoeff(ctx context.Context, r *CurrentCoeffRecord) error {
	return m.WithTx(ctx, func(tx Tx) error { return tx.PutCurrentCoeff(ctx, r) })
}
func (m *MySQL) ListCurrentCoeffs(ctx context.Context, machineID string) ([]CurrentCoeffRecord, error) {
	var out []CurrentCoeffRecord
	err := m.WithTx(ctx, func(tx Tx) error {
		var e error
		out, e = tx.ListCurrentCoeffs(ctx, machineID)
		return e
	})
	return out, err
}

func (m *MySQL) DeleteCurrentCoeffsNotIn(ctx context.Context, machineID string, keep []int) error {
	return m.WithTx(ctx, func(tx Tx) error { return tx.DeleteCurrentCoeffsNotIn(ctx, machineID, keep) })
}

func (m *MySQL) DeleteEstimatesForJob(ctx context.Context, jobID string) error {
	return m.WithTx(ctx, func(tx Tx) error { return tx.DeleteEstimatesForJob(ctx, jobID) })
}
