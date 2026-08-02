import * as path from 'path';
import * as fs from 'fs';
import * as vscode from 'vscode';
import { LanguageClient, LanguageClientOptions, ServerOptions, TransportKind } from 'vscode-languageclient/node';

let client: LanguageClient | undefined;

export function activate(context: vscode.ExtensionContext) {
	const serverModule = process.platform === 'win32' ? 'construct-lsp.exe' : 'construct-lsp';

	// Resolve the server binary. Priority:
	// 1. CONSTRUCT_LSP_DEV env var (explicit dev path)
	// 2. A .construct-lsp-dev marker file in the workspace root (dev mode)
	// 3. The installed extension's server/ directory (production)
	let serverPath: string;
	const devEnv = process.env.CONSTRUCT_LSP_DEV;
	const markerPath = path.join(vscode.workspace.rootPath || '', '.construct-lsp-dev');

	if (devEnv) {
		serverPath = path.join(devEnv, serverModule);
	} else if (vscode.workspace.rootPath && fs.existsSync(markerPath)) {
		// Marker file contains the path to the server directory.
		const devDir = fs.readFileSync(markerPath, 'utf8').trim();
		serverPath = path.join(devDir, serverModule);
	} else {
		serverPath = path.join(context.extensionPath, 'server', serverModule);
	}

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
