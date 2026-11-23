// Copyright (c) 2025 Reliant Labs

package db

import "github.com/reliant-labs/reliant/internal/credentials/accountseal"

func ProviderAccountAAD(accountID, column string) []byte { return accountseal.AAD(accountID, column) }
