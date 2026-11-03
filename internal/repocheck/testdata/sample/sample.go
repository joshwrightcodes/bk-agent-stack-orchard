// SPDX-License-Identifier: AGPL-3.0-or-later

// Package sample is the package that the test of Load type-checks.
package sample

import "os"

const prefix = "sample."

// Joined takes its value from an expression of this package.
const Joined = prefix + "name"

// Separator takes its value from an import, which only the export data of os gives.
const Separator = string(os.PathSeparator)
