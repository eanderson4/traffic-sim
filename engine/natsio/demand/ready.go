package demand

// ready.go — the attach-barrier readiness signal (serve parks the run loop
// at tick 0 via ContractConfig.StartGate until every embedded client is
// ready).

// Ready flushes the director's connection: after it returns, the server has
// processed the snapshot subscription that drives the verb loop, so no
// snapshot — tick 0's included — can slip past unheard. Attach subscribes
// without a final flush; serve's attach barrier calls Ready before opening
// the start gate. (The default driver needs no equivalent: driver.New's
// final Flush already covers its subscriptions.)
func (d *Director) Ready() error {
	return d.nc.Flush()
}
