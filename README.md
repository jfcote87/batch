Google Client Api Batch Utility in Go

The batch package implements a batch service that processes client api calls from various client api go packages and send the queued calls as a single batch call.  Individual responses may then be interrogated for errors and the responses processed.  For specifics of the batch protocol see
https://cloud.google.com/storage/docs/json_api/v1/how-tos/batch

Examples may be found in the example_test.go file.
