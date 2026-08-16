import * as path from 'path';
import * as fs from 'fs';
import * as vscode from 'vscode';
import { LanguageClient, LanguageClientOptions, ServerOptions, TransportKind } from 'vscode-languageclient/node';

let client: LanguageClient | undefined;

export function activate(context: vscode.ExtensionContext) {
	const serverModule = process.platform === 'win32' ? 'construct-lsp.exe' : 'construct-lsp';

	let serverPath: string;
	const devEnv = process.env.CONSTRUCT_LSP_DEV;
	const workspaceRoot = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
	const markerPath = workspaceRoot ? path.join(workspaceRoot, '.construct-lsp-dev') : undefined;

	if (devEnv) {
		serverPath = path.join(devEnv, serverModule);
	} else if (markerPath && fs.existsSync(markerPath)) {
		let devDir = fs.readFileSync(markerPath, 'utf8').trim();
		if (devDir && !path.isAbsolute(devDir)) {
			devDir = path.join(workspaceRoot!, devDir);
		}
		serverPath = path.join(devDir, serverModule);
	} else {
		serverPath = path.join(context.extensionPath, 'server', serverModule);
	}

	if (!fs.existsSync(serverPath)) {
		void vscode.window.showErrorMessage(
			`Constfile language server not found at ${serverPath}. ` +
			'The extension may be installed for a different platform — reinstall it from a matching VSIX.');
		return;
	}

	const serverOptions: ServerOptions = {
		run: { command: serverPath, transport: TransportKind.stdio },
		debug: { command: serverPath, transport: TransportKind.stdio },
	};

	const clientOptions: LanguageClientOptions = {
		documentSelector: [{ scheme: 'file', language: 'constfile' }],
	};

	client = new LanguageClient('constfile', 'Constfile Language Server', serverOptions, clientOptions);
	void client.start().catch((err) => {
		void vscode.window.showErrorMessage(`Constfile language server failed to start: ${err}`);
	});
}

export function deactivate(): Thenable<void> | undefined {
	if (!client) {
		return undefined;
	}
	return client.stop();
}
