import * as vscode from "vscode";
import {
    InsertTextFormat,
    LanguageClient,
    Position,
    TextEdit,
} from "vscode-languageclient/node";
import {
    Condition,
    conditionalRegistration,
} from "./util/dependentRegistration";

const AUTO_INSERT_DELAY = 100;

interface VsOnAutoInsertParams {
    _vs_textDocument: { uri: string; };
    _vs_position: Position;
    _vs_ch: string;
}

interface VsOnAutoInsertResponse {
    _vs_textEditFormat: InsertTextFormat;
    _vs_textEdit: TextEdit;
}

interface VsOnAutoInsertOptions {
    _vs_triggerCharacters?: string[];
}

interface VsServerCapabilities {
    _vs_onAutoInsertProvider?: VsOnAutoInsertOptions;
}

class AutoInsert {
    private disposed = false;
    private timeout: NodeJS.Timeout | undefined;
    private cancel: vscode.CancellationTokenSource | undefined;
    private onDidChangeSubscription: vscode.Disposable | undefined;
    private readonly client: LanguageClient;
    private readonly triggerCharacters: ReadonlySet<string>;

    constructor(client: LanguageClient, triggerCharacters: readonly string[]) {
        this.client = client;
        this.triggerCharacters = new Set(triggerCharacters);
        this.onDidChangeSubscription = vscode.workspace.onDidChangeTextDocument(
            this.onDidChangeTextDocument,
            this,
        );
    }

    dispose() {
        this.disposed = true;

        this.onDidChangeSubscription?.dispose();
        this.onDidChangeSubscription = undefined;

        if (this.timeout) {
            clearTimeout(this.timeout);
            this.timeout = undefined;
        }

        if (this.cancel) {
            this.cancel.cancel();
            this.cancel.dispose();
            this.cancel = undefined;
        }
    }

    onDidChangeTextDocument({ document, contentChanges, reason }: vscode.TextDocumentChangeEvent) {
        if (contentChanges.length === 0 || reason === vscode.TextDocumentChangeReason.Undo || reason === vscode.TextDocumentChangeReason.Redo) {
            return;
        }

        const activeDocument = vscode.window.activeTextEditor?.document;
        if (document !== activeDocument) {
            return;
        }

        if (typeof this.timeout !== "undefined") {
            clearTimeout(this.timeout);
        }

        if (this.cancel) {
            this.cancel.cancel();
            this.cancel.dispose();
            this.cancel = undefined;
        }

        const lastChange = contentChanges[contentChanges.length - 1];
        const lastCharacter = lastChange.text.charAt(lastChange.text.length - 1);
        if (lastChange.rangeLength > 0 || !this.triggerCharacters.has(lastCharacter)) {
            return;
        }

        const startingVersion = document.version;
        this.timeout = setTimeout(async () => {
            this.timeout = undefined;

            if (this.disposed) {
                return;
            }

            const addedLines = lastChange.text.split(/\r\n|\n/g);
            const position = addedLines.length <= 1
                ? lastChange.range.start.translate(0, lastChange.text.length)
                : new vscode.Position(
                    lastChange.range.start.line + addedLines.length - 1,
                    addedLines[addedLines.length - 1].length,
                );

            const params: VsOnAutoInsertParams = {
                _vs_textDocument: this.client.code2ProtocolConverter.asTextDocumentIdentifier(document),
                _vs_position: this.client.code2ProtocolConverter.asPosition(position),
                _vs_ch: lastCharacter,
            };
            this.cancel = new vscode.CancellationTokenSource();

            let response: VsOnAutoInsertResponse | null;
            try {
                response = await this.client.sendRequest<VsOnAutoInsertResponse | null>(
                    "textDocument/_vs_onAutoInsert",
                    params,
                    this.cancel.token,
                );
            }
            catch (e) {
                console.error("Error requesting auto-insert:", e);
                return;
            }

            if (!response || this.disposed) {
                return;
            }

            const activeEditor = vscode.window.activeTextEditor;
            if (activeEditor === undefined) {
                return;
            }

            const activeDocument = activeEditor.document;
            if (document !== activeDocument || activeDocument.version !== startingVersion) {
                return;
            }

            const edit = this.client.protocol2CodeConverter.asTextEdit(response._vs_textEdit);
            if (response._vs_textEditFormat === InsertTextFormat.Snippet) {
                activeEditor.insertSnippet(new vscode.SnippetString(edit.newText), edit.range);
            }
            else {
                activeEditor.edit(editBuilder => editBuilder.replace(edit.range, edit.newText));
            }
        }, AUTO_INSERT_DELAY);
    }
}

function requireActiveDocumentSetting(languageConfigSection: "typescript" | "javascript", selector: vscode.DocumentSelector) {
    return new Condition(
        () => {
            const activeEditor = vscode.window.activeTextEditor;
            if (!activeEditor) {
                return false;
            }

            const activeDocument = activeEditor.document;
            if (!vscode.languages.match(selector, activeDocument)) {
                return false;
            }

            const autoClosingTags = vscode.workspace.getConfiguration(languageConfigSection, activeDocument).get("autoClosingTags");
            return !!autoClosingTags;
        },
        handler => {
            return vscode.Disposable.from(
                vscode.window.onDidChangeActiveTextEditor(handler),
                vscode.workspace.onDidOpenTextDocument(handler),
                vscode.workspace.onDidChangeConfiguration(handler),
            );
        },
    );
}

export function registerOnAutoInsertFeature(
    languageConfigSection: "typescript" | "javascript",
    selector: vscode.DocumentSelector,
    client: LanguageClient,
): vscode.Disposable {
    const capabilities = client.initializeResult?.capabilities as VsServerCapabilities | undefined;
    const triggerCharacters = capabilities?._vs_onAutoInsertProvider?._vs_triggerCharacters;
    if (!triggerCharacters || triggerCharacters.length === 0) {
        return vscode.Disposable.from();
    }
    return conditionalRegistration([
        requireActiveDocumentSetting(languageConfigSection, selector),
    ], () => new AutoInsert(client, triggerCharacters));
}
