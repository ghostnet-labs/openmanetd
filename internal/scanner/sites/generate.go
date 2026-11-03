package sites

// The embedded table is generated from the GHO-60 research CSVs copied
// into testfixtures/scanner/sites. Regenerate after editing them:
//
//	go generate ./internal/scanner/sites
//
//go:generate go run ./sitegen -fit ../../../testfixtures/scanner/sites/site-tuner-fit.csv -freq ../../../testfixtures/scanner/sites/scanner-frequencies.csv -out sites.dat.gz
