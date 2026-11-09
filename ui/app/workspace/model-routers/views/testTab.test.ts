import { describe, expect, test } from "vitest";
import { parseTestStatements } from "./testTab";

describe("test statements", () => {
	test("reads one statement per line, skipping blank lines", () => {
		expect(parseTestStatements("hello\n\n  summarize this thread  \n")).toEqual([
			{ id: "1", text: "hello" },
			{ id: "2", text: "summarize this thread" },
		]);
	});

	test("takes a leading tier, in any case, as the tier the statement should get", () => {
		expect(parseTestStatements("simple: hi\nCOMPLEX:design a cache\nMedium : fix this function")).toEqual([
			{ id: "1", text: "hi", expected_tier: "SIMPLE" },
			{ id: "2", text: "design a cache", expected_tier: "COMPLEX" },
			{ id: "3", text: "fix this function", expected_tier: "MEDIUM" },
		]);
	});

	test("drops a line that is only a label and keeps a colon that is not one", () => {
		expect(parseTestStatements("SIMPLE:\nnote: keep this")).toEqual([{ id: "2", text: "note: keep this" }]);
	});
});