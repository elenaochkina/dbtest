// Package temporal holds what the workflow and activity packages share.
//
// temporal/workflows   durable scenario orchestration
// temporal/activities  the side-effecting step logic they schedule
//
// The dependency runs one way: workflows import activities, never the reverse.
package temporal

const TaskQueue = "dbtest-tq"
