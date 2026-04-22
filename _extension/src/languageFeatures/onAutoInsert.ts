import * as vscode from "vscode";
import {
    InsertTextFormat,
    LanguageClient,
    Position,
    TextEdit,
} from "vscode-languageclient/node";
import { readUnifiedConfig } from "../util";
import {
    Condition,
    conditionalRegistration,
} from "./util/dependentRegistration";

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
    private cancel: vscode.CancellationTokenSource | undefined;
    private readonly subscription: vscode.Disposable;

    constructor(
        private readonly client: LanguageClient,
        private readonly triggerCharacters: ReadonlySet<string>,
    ) {
        this.subscription = vscode.workspace.onDidChangeTextDocument(this.onDidChangeTextDocument, this);
    }

    dispose() {
        this.subscription.dispose();
        this.cancel?.cancel();
        this.cancel?.dispose();
        this.cancel = undefined;
    }

    private async onDidChangeTextDocument({ document, contentChanges, reason }: vscode.TextDocumentChangeEvent) {
        if (
            contentChanges.length === 0
            || reason === vscode.TextDocumentChangeReason.Undo
            || reason === vscode.TextDocumentChangeReason.Redo
        ) {
            return;
        }

        const activeEditor = vscode.window.activeTextEditor;
        if (activeEditor?.document !== document) {
            return;
        }

        const lastChange = contentChanges[contentChanges.length - 1];
        const lastCharacter = lastChange.text.charAt(lastChange.text.length - 1);
        if (lastChange.rangeLength > 0 || !this.triggerCharacters.has(lastCharacter)) {
            return;
        }

        // Compute the post-insert cursor position from the change itself;
        // `activeEditor.selection.active` is not yet updated when this event fires.
        const addedLines = lastChange.text.split(/\r\n|\n/g);
        const position = addedLines.length === 1
            ? lastChange.range.start.translate(0, lastChange.text.length)
            : new vscode.Position(
                lastChange.range.start.line + addedLines.length - 1,
                addedLines[addedLines.length - 1].length,
            );

        this.cancel?.cancel();
        this.cancel?.dispose();
        this.cancel = new vscode.CancellationTokenSource();
        const token = this.cancel.token;
        const startingVersion = document.version;

        let response: VsOnAutoInsertResponse | null;
        try {
            response = await this.client.sendRequest<VsOnAutoInsertResponse | null>(
                "textDocument/_vs_onAutoInsert",
                {
                    _vs_textDocument: this.client.code2ProtocolConverter.asTextDocumentIdentifier(document),
                    _vs_position: this.client.code2ProtocolConverter.asPosition(position),
                    _vs_ch: lastCharacter,
                } satisfies VsOnAutoInsertParams,
                token,
            );
        }
        catch {
            return;
        }

        if (!response || token.isCancellationRequested || document.version !== startingVersion) {
            return;
        }

        const edit = this.client.protocol2CodeConverter.asTextEdit(response._vs_textEdit);
        if (response._vs_textEditFormat === InsertTextFormat.Snippet) {
            activeEditor.insertSnippet(new vscode.SnippetString(edit.newText), edit.range);
        }
        else {
            activeEditor.edit(b => b.replace(edit.range, edit.newText));
        }
    }
}

function isAutoClosingTagsEnabled(scope: vscode.TextDocument): boolean {
    // Use the document's own language ("typescript" / "javascript" / "*react") to
    // pick the fallback section so per-language overrides apply correctly.
    const fallbackSection = scope.languageId === "javascript" || scope.languageId === "javascriptreact"
        ? "javascript"
        : "typescript";
    return readUnifiedConfig("autoClosingTags.enabled", fallbackSection, "autoClosingTags", scope, true);
}

function requireActiveDocumentSetting(selector: vscode.DocumentSelector) {
    return new Condition(
        () => {
            const activeDocument = vscode.window.activeTextEditor?.document;
            if (!activeDocument || !vscode.languages.match(selector, activeDocument)) {
                return false;
            }
            return isAutoClosingTagsEnabled(activeDocument);
        },
        handler =>
            vscode.Disposable.from(
                vscode.window.onDidChangeActiveTextEditor(handler),
                vscode.workspace.onDidOpenTextDocument(handler),
                vscode.workspace.onDidChangeConfiguration(handler),
            ),
    );
}

export function registerOnAutoInsertFeature(
    selector: vscode.DocumentSelector,
    client: LanguageClient,
): vscode.Disposable {
    const capabilities = client.initializeResult?.capabilities as VsServerCapabilities | undefined;
    const triggerCharacters = capabilities?._vs_onAutoInsertProvider?._vs_triggerCharacters;
    if (!triggerCharacters?.length) {
        return vscode.Disposable.from();
    }
    const set = new Set(triggerCharacters);
    return conditionalRegistration(
        [requireActiveDocumentSetting(selector)],
        () => new AutoInsert(client, set),
    );
}
