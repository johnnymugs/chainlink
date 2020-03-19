package adapters

import (
	"encoding/json"
	"fmt"
	"net"
	"regexp"

	"chainlink/core/eth"
	"chainlink/core/logger"
	strpkg "chainlink/core/store"
	"chainlink/core/store/models"
	"chainlink/core/utils"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/pkg/errors"
	"gopkg.in/guregu/null.v3"
)

const (
	// DataFormatBytes instructs the EthTx Adapter to treat the input value as a
	// bytes string, rather than a hexadecimal encoded bytes32
	DataFormatBytes = "bytes"
)

// EthTx holds the Address to send the result to and the FunctionSelector
// to execute.
type EthTx struct {
	Address          common.Address       `json:"address"`
	FunctionSelector eth.FunctionSelector `json:"functionSelector"`
	DataPrefix       hexutil.Bytes        `json:"dataPrefix"`
	DataFormat       string               `json:"format"`
	GasPrice         *utils.Big           `json:"gasPrice" gorm:"type:numeric"`
	GasLimit         uint64               `json:"gasLimit"`
}

// Perform creates the run result for the transaction if the existing run result
// is not currently pending. Then it confirms the transaction was confirmed on
// the blockchain.
func (etx *EthTx) Perform(input models.RunInput, store *strpkg.Store) models.RunOutput {
	if !store.TxManager.Connected() {
		return pendingConfirmationsOrConnection(input)
	}

	if input.Status().PendingConfirmations() {
		return ensureTxRunResult(input, store)
	}

	value, err := getTxData(etx, input)
	if err != nil {
		err = errors.Wrap(err, "while constructing EthTx data")
		return models.NewRunOutputError(err)
	}

	data := utils.ConcatBytes(etx.FunctionSelector.Bytes(), etx.DataPrefix, value)
	return createTxRunResult(etx.Address, etx.GasPrice, etx.GasLimit, data, input, store)
}

// getTxData returns the data to save against the callback encoded according to
// the dataFormat parameter in the job spec
func getTxData(e *EthTx, input models.RunInput) ([]byte, error) {
	result := input.Result()
	if e.DataFormat == "" {
		return common.HexToHash(result.Str).Bytes(), nil
	}

	payloadOffset := utils.EVMWordUint64(utils.EVMWordByteLen)
	if len(e.DataPrefix) > 0 {
		payloadOffset = utils.EVMWordUint64(utils.EVMWordByteLen * 2)
	}
	output, err := utils.EVMTranscodeJSONWithFormat(result, e.DataFormat)
	if err != nil {
		return []byte{}, err
	}
	return utils.ConcatBytes(payloadOffset, output), nil
}

func createTxRunResult(
	address common.Address,
	gasPrice *utils.Big,
	gasLimit uint64,
	data []byte,
	input models.RunInput,
	store *strpkg.Store,
) models.RunOutput {
	tx, err := store.TxManager.CreateTxWithGas(
		null.StringFrom(input.JobRunID().String()),
		address,
		data,
		gasPrice.ToInt(),
		gasLimit,
	)

	if err != nil {
		// TODO: Log error somehow? Prometheus metric?
		// We need to know if this is happening a lot
		return models.NewRunOutputPendingConfirmationsWithData(input.Data())
	}

	output, err := models.JSON{}.Add("result", tx.Hash.String())
	if err != nil {
		return models.NewRunOutputError(err)
	}

	// txAttempt := tx.Attempts[0]
	// receipt, state, err := store.TxManager.CheckAttempt(txAttempt, tx.SentAt)
	// if err != nil {
	//     return models.NewRunOutputPendingConfirmationsWithData(output)
	// }

	// logger.Debugw(
	//     fmt.Sprintf("Tx #0 is %s", state),
	//     "txHash", txAttempt.Hash.String(),
	//     "txID", txAttempt.TxID,
	//     "receiptBlockNumber", receipt.BlockNumber.ToInt(),
	//     "currentBlockNumber", tx.SentAt,
	//     "receiptHash", receipt.Hash.Hex(),
	// )

	// if state == strpkg.Safe {
	//     return addReceiptToResult(receipt, input, output)
	// }

	return models.NewRunOutputPendingConfirmationsWithData(output)
}

func ensureTxRunResult(input models.RunInput, str *strpkg.Store) models.RunOutput {
	val, err := input.ResultString()
	if err != nil {
		return models.NewRunOutputError(err)
	}
	hash := common.HexToHash(val)

	tx, _, err := str.ORM.FindTxByAttempt(hash)
	if err != nil {
		return models.NewRunOutputError(err)
	}

	var output models.JSON
	output, err = output.Add("result", tx.Hash.String())
	if err != nil {
		return models.NewRunOutputError(err)
	}

	if tx.Failed {
		return models.NewRunOutputError(errors.New("transaction never succeeded"))

	} else if !tx.Confirmed {
		// FIXME: If the tx is still unconfirmed, just copy over the original
		// tx hash. This seems pointless
		output, err = output.Add("latestOutgoingTxHash", tx.Hash.String())
		if err != nil {
			return models.NewRunOutputError(err)
		}
		return models.NewRunOutputPendingConfirmationsWithData(output)

	} else {
		receipt, err := str.TxManager.GetTxReceipt(tx.Hash)
		if err != nil {
			return models.NewRunOutputError(err)
		}
		return addReceiptToResult(receipt, input, output)
	}
}

func addReceiptToResult(
	receipt *eth.TxReceipt,
	input models.RunInput,
	data models.JSON,
) models.RunOutput {
	receipts := []eth.TxReceipt{}

	ethereumReceipts := input.Data().Get("ethereumReceipts").String()
	if ethereumReceipts != "" {
		if err := json.Unmarshal([]byte(ethereumReceipts), &receipts); err != nil {
			logger.Errorw("Error unmarshaling ethereum Receipts", "error", err)
		}
	}

	if receipt == nil {
		err := errors.New("missing receipt for transaction")
		return models.NewRunOutputError(err)
	}

	receipts = append(receipts, *receipt)
	var err error
	data, err = data.Add("ethereumReceipts", receipts)
	if err != nil {
		return models.NewRunOutputError(err)
	}
	data, err = data.Add("result", receipt.Hash.String())
	if err != nil {
		return models.NewRunOutputError(err)
	}
	return models.NewRunOutputComplete(data)
}

func pendingConfirmationsOrConnection(input models.RunInput) models.RunOutput {
	// If the input is not pending confirmations next time
	// then it may submit a new transaction.
	if input.Status().PendingConfirmations() {
		return models.NewRunOutputPendingConfirmationsWithData(input.Data())
	}
	return models.NewRunOutputPendingConnection()
}
