Question	Hardcoded factory	Registration
Adding a new scenario	2 files change

Edit scenario.go to add a new case, plus create the new scenario file	1 file only

Create the new scenario file with its own init() — scenario.go never changes
When scenarios are loaded into memory	On demand

Only the requested scenario is constructed. Others are never touched	All at startup

Every init() in the package runs when the package is imported, before main()
Risk of expensive init()	None

No init() involved at all	Possible

If a future scenario does real work in init() (network, disk), every test pays that cost. In practice: avoidable by keeping init() cheap — just call Register(), nothing else
Spelling mistake in scenario name	Runtime error

New("warehuse", cfg) hits the default case and returns an error when the test runs	Runtime error

Same — New("warehuse", cfg) fails to find the key in the map and returns an error when the test runs
Forgetting to register a new scenario	Silent gap

You add chaos.go but forget to add its case — New("chaos") returns an error at runtime	Silent gap

You add chaos.go but forget to call Register() — same result, runtime error
Fits dbtest?	Not ideal

Scenario list grows over time, multiple contributors — editing scenario.go every time is a bottleneck	Yes

Each scenario author owns one file. scenario.go stays stable. init() stays cheap (just Register())