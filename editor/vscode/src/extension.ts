import * as path from 'path';
import * as vscode from 'vscode';
import { LanguageClient, LanguageClientOptions, ServerOptions, TransportKind } from 'vscode-languageclient/node';

let client: LanguageClient | undefined;

export function activate(context: vscode.ExtensionContext) {
	// The Go LSP server binary. On first run the user must build it (see README);
	// we resolve the binary name per-platform.
	const serverModule = process.platform === 'win32' ? 'construct-lsp.exe' : 'construct-lsp';
	const serverPath = path.join(context.extensionPath, 'server', serverModule);

	const serverOptions: ServerOptions = {
		run: { command: serverPath, transport: TransportKind.stdio },
		debug: { command: serverPath, transport: TransportKind.stdio },
	};

	const clientOptions: LanguageClientOptions = {
		documentSelector: [{ scheme: 'file', language: 'constfile' }],
		synchronize: {},
	};

	client = new LanguageClient('constfile', 'Constfile Language Server', serverOptions, clientOptions);
	client.start();
}

export function deactivate(): Thenable<void> | undefined {
	if (!client) {
		return undefined;
	}
	return client.stop();
}
